package server

import (
	"bytes"
	"compress/gzip"
	"fmt"
	// "io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type HandlerReq struct {
	w    http.ResponseWriter
	r    *http.Request
	Rpc  string
	Dir  string
	File string
}

type Service struct {
	Method  string
	Handler func(HandlerReq)
	Rpc     string
}

type Config struct {
	AuthPassEnvVar string
	AuthUserEnvVar string
	DefaultEnv     string
	ProjectRoot    string
	GitBinPath     string
	UploadPack     bool
	ReceivePack    bool
	RoutePrefix    string
	CommandFunc    func(*exec.Cmd)
}

var (
	DefaultAddress = ":8600"

	DefaultConfig = Config{
		AuthPassEnvVar: "",
		AuthUserEnvVar: "",
		DefaultEnv:     "",
		ProjectRoot:    "",
		GitBinPath:     "/usr/local/bin/git",
		UploadPack:     true,
		ReceivePack:    true,
		RoutePrefix:    "",
		CommandFunc:    func(*exec.Cmd) {},
	}
)

func init() {
	dir, err := os.Getwd()
	if err == nil {
		DefaultConfig.ProjectRoot = fmt.Sprintf("%s/repo", dir)
	}
}

var services = map[string]Service{
	"(.*?)/git-upload-pack$":                       Service{"POST", serviceRpc, "upload-pack"},
	"(.*?)/git-receive-pack$":                      Service{"POST", serviceRpc, "receive-pack"},
	"(.*?)/info/refs$":                             Service{"GET", getInfoRefs, ""},
	"(.*?)/HEAD$":                                  Service{"GET", getTextFile, ""},
	"(.*?)/objects/info/alternates$":               Service{"GET", getTextFile, ""},
	"(.*?)/objects/info/http-alternates$":          Service{"GET", getTextFile, ""},
	"(.*?)/objects/info/packs$":                    Service{"GET", getInfoPacks, ""},
	"(.*?)/objects/info/[^/]*$":                    Service{"GET", getTextFile, ""},
	"(.*?)/objects/[0-9a-f]{2}/[0-9a-f]{38}$":      Service{"GET", getLooseObject, ""},
	"(.*?)/objects/pack/pack-[0-9a-f]{40}\\.pack$": Service{"GET", getPackFile, ""},
	"(.*?)/objects/pack/pack-[0-9a-f]{40}\\.idx$":  Service{"GET", getIdxFile, ""},
}

func getInfoRefs(hr HandlerReq) {
	hdrNocache(hr.w)
	w, r, dir := hr.w, hr.r, hr.Dir
	service_name := getServiceType(r)

	if service_name != "upload-pack" && service_name != "receive-pack" {
		updateServerInfo(hr.Dir)
		sendFile("text/plain; charset=utf-8", hr)
		return
	}

	version := r.Header.Get("Git-Protocol")
	args := []string{service_name, "--stateless-rpc", "--advertise-refs", "."}
	refs := gitCommand(dir, version, args...)

	w.Header().Set("Content-Type", fmt.Sprintf("application/x-git-%s-advertisement", service_name))
	w.WriteHeader(http.StatusOK)

	w.Write(packetWrite("# service=git-" + service_name + "\n"))
	w.Write([]byte("0000"))
	w.Write(refs)
}

func updateServerInfo(dir string) []byte {
	fmt.Println("updateServerInfo", dir)
	args := []string{"update-server-info"}
	return gitCommand(dir, "", args...)
}

func getInfoPacks(hr HandlerReq) {
	fmt.Println("getInfoPacks")
	hdrCacheForever(hr.w)
	sendFile("text/plain; charset=utf-8", hr)
}

func getLooseObject(hr HandlerReq) {
	hdrCacheForever(hr.w)
	sendFile("application/x-git-loose-object", hr)
}

func getTextFile(hr HandlerReq) {
	fmt.Println("getTextFile")
	hdrNocache(hr.w)
	sendFile("text/plain", hr)
}

func sendFile(content_type string, hr HandlerReq) {
	w, r := hr.w, hr.r
	req_file := path.Join(hr.Dir, hr.File)

	f, err := os.Stat(req_file)
	if os.IsNotExist(err) {
		renderNotFound(w)
		return
	}

	w.Header().Set("Content-Type", content_type)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", f.Size()))
	w.Header().Set("Last-Modified", f.ModTime().Format(http.TimeFormat))
	http.ServeFile(w, r, req_file)
}

func getIdxFile(hr HandlerReq) {
	fmt.Println("getIdxFile")
	hdrCacheForever(hr.w)
	sendFile("application/x-git-packed-objects-toc", hr)
}

func getPackFile(hr HandlerReq) {
	fmt.Println("getPackFile")
	hdrCacheForever(hr.w)
	sendFile("application/x-git-packed-objects", hr)
}

func hdrNocache(w http.ResponseWriter) {
	w.Header().Set("Expires", "Fri, 01 Jan 1980 00:00:00 GMT")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Cache-Control", "no-cache, max-age=0, must-revalidate")
}

func hdrCacheForever(w http.ResponseWriter) {
	now := time.Now().Unix()
	expires := now + 31536000
	w.Header().Set("Date", fmt.Sprintf("%d", now))
	w.Header().Set("Expires", fmt.Sprintf("%d", expires))
	w.Header().Set("Cache-Control", "public, max-age=31536000")
}

func getServiceType(r *http.Request) string {
	service_type := r.FormValue("service")

	if s := strings.HasPrefix(service_type, "git-"); !s {
		return ""
	}
	return strings.Replace(service_type, "git-", "", 1)
}

func gitCommand(dir string, version string, args ...string) []byte {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		log.Println(fmt.Sprintf("Git: %v - %s", err, out))
	}
	return out
}

func packetWrite(str string) []byte {
	s := strconv.FormatInt(int64(len(str)+4), 16)
	if len(s)%4 != 0 {
		s = strings.Repeat("0", 4-len(s)%4) + s
	}
	return []byte(s + str)
}

func ComposeHookEnvs() []string {
	envs := []string{
		"SSH_ORIGINAL_COMMAND=1",
	}
	return envs
}

// Actual command handling functions
func serviceRpc(hr HandlerReq) {
	defer hr.r.Body.Close()

	w, r, rpc := hr.w, hr.r, hr.Rpc

	repoDir := fmt.Sprintf("%s", hr.Dir)
	if r.Header.Get("Content-Type") != fmt.Sprintf("application/x-git-%s-request", rpc) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	hr.w.Header().Set("Content-Type", fmt.Sprintf("application/x-git-%s-result", rpc))

	var (
		reqBody = r.Body
		err     error
	)
	// Handle GZIP
	if hr.r.Header.Get("Content-Encoding") == "gzip" {
		reqBody, err = gzip.NewReader(reqBody)
		if err != nil {
			log.Printf("HTTP.Get: fail to create gzip reader: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}

	var stderr bytes.Buffer
	cmd := exec.Command("git", rpc, "--stateless-rpc", repoDir)

	if rpc == "receive-pack" {
		cmd.Env = append(os.Environ(), ComposeHookEnvs()...)
	}

	cmd.Dir = repoDir
	cmd.Stdout = hr.w
	cmd.Stderr = &stderr
	cmd.Stdin = reqBody
	if err = cmd.Run(); err != nil {
		log.Printf("HTTP.serviceRPC: fail to serve RPC '%s': %v - %s", rpc, err, stderr.String())
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

}

func renderNotFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte("Not Found"))
}

func renderNoAccess(w http.ResponseWriter) {
	w.WriteHeader(http.StatusForbidden)
	w.Write([]byte("Forbidden"))
}

func renderMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	if r.Proto == "HTTP/1.1" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Write([]byte("Method Not Allowed"))
	} else {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad Request"))
	}
}

func getRepoGitDir(file_path string) (string, error) {
	root := DefaultConfig.ProjectRoot
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			log.Print(err)
			return "", err
		}
		root = cwd
	}

	f := path.Join(root, file_path)
	if _, err := os.Stat(f); os.IsNotExist(err) {
		return "", err
	}

	return f, nil
}

// Request handling function
func Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s %s %s", r.RemoteAddr, r.Method, r.URL.Path, r.Proto)
		for match, service := range services {
			re, err := regexp.Compile(match)
			if err != nil {
				log.Println(err)
			}

			if m := re.FindStringSubmatch(r.URL.Path); m != nil {
				if service.Method != r.Method {
					renderMethodNotAllowed(w, r)
					return
				}

				rpc := service.Rpc
				file := strings.Replace(r.URL.Path, m[1]+"/", "", 1)
				dir, err := getRepoGitDir(m[1])

				if err != nil {
					log.Println(err)
					renderNotFound(w)
					return
				}

				hr := HandlerReq{w, r, rpc, dir, file}
				service.Handler(hr)
				return
			}
		}
		renderNotFound(w)
		return
	}
}
