package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	port         = 8080
	nodeJSURL    = "http://localhost:3000"
	workDir      = "./workspace"
	cacheDir     = "./cache"
	pluginsDir   = "../python-plugins"
)

type BuildRequest struct {
	TaskID   string   `json:"taskId"`
	Repo     string   `json:"repo"`
	Branch   string   `json:"branch"`
	Commands []string `json:"commands"`
	Cache    []string `json:"cache"`
	Timeout  int      `json:"timeout"`
	Plugins  []string `json:"plugins"`
}

type LogRequest struct {
	Log    string `json:"log"`
	Status string `json:"status"`
}

var (
	buildQueue = make(chan BuildRequest, 100)
	wg         sync.WaitGroup
)

func main() {
	os.MkdirAll(workDir, 0755)
	os.MkdirAll(cacheDir, 0755)

	go processBuildQueue()

	r := gin.Default()
	r.POST("/build", handleBuild)
	r.Run(fmt.Sprintf(":%d", port))
}

func handleBuild(c *gin.Context) {
	var req BuildRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	buildQueue <- req
	c.JSON(http.StatusOK, gin.H{"status": "queued"})
}

func processBuildQueue() {
	for req := range buildQueue {
		wg.Add(1)
		go func(b BuildRequest) {
			defer wg.Done()
			executeBuild(b)
		}(req)
	}
}

func executeBuild(req BuildRequest) {
	sendLog(req.TaskID, "Starting build...", "running")
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(req.Timeout)*time.Second)
	defer cancel()

	taskDir := filepath.Join(workDir, req.TaskID)
	os.MkdirAll(taskDir, 0755)
	defer os.RemoveAll(taskDir)

	repoURL := fmt.Sprintf("https://github.com/%s.git", req.Repo)
	if strings.Contains(req.Repo, "gitlab.com") {
		repoURL = fmt.Sprintf("https://gitlab.com/%s.git", req.Repo)
	}

	sendLog(req.TaskID, fmt.Sprintf("Cloning repository: %s", repoURL), "")
	if err := runCommand(ctx, taskDir, req.TaskID, "git", "clone", "--depth", "1", "--branch", req.Branch, repoURL, "."); err != nil {
		sendLog(req.TaskID, fmt.Sprintf("Clone failed: %v", err), "failed")
		return
	}

	for _, cachePath := range req.Cache {
		src := filepath.Join(cacheDir, req.Repo, cachePath)
		dst := filepath.Join(taskDir, cachePath)
		if _, err := os.Stat(src); err == nil {
			sendLog(req.TaskID, fmt.Sprintf("Restoring cache: %s", cachePath), "")
			copyDir(src, dst)
		}
	}

	for _, cmd := range req.Commands {
		sendLog(req.TaskID, fmt.Sprintf("Executing: %s", cmd), "")
		if err := runShellCommand(ctx, taskDir, req.TaskID, cmd); err != nil {
			sendLog(req.TaskID, fmt.Sprintf("Command failed: %v", err), "failed")
			return
		}
	}

	for _, plugin := range req.Plugins {
		sendLog(req.TaskID, fmt.Sprintf("Running plugin: %s", plugin), "")
		pluginPath := filepath.Join(pluginsDir, plugin+".py")
		if _, err := os.Stat(pluginPath); err == nil {
			if err := runCommand(ctx, taskDir, req.TaskID, "python", pluginPath); err != nil {
				sendLog(req.TaskID, fmt.Sprintf("Plugin failed: %v", err), "failed")
				return
			}
		} else {
			sendLog(req.TaskID, fmt.Sprintf("Plugin not found: %s", plugin), "")
		}
	}

	for _, cachePath := range req.Cache {
		src := filepath.Join(taskDir, cachePath)
		dst := filepath.Join(cacheDir, req.Repo, cachePath)
		if _, err := os.Stat(src); err == nil {
			sendLog(req.TaskID, fmt.Sprintf("Saving cache: %s", cachePath), "")
			os.MkdirAll(filepath.Dir(dst), 0755)
			copyDir(src, dst)
		}
	}

	sendLog(req.TaskID, "Build completed successfully!", "success")
}

func runCommand(ctx context.Context, dir string, taskID string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	go func() {
		io.Copy(&logWriter{taskID: taskID}, stdout)
	}()
	go func() {
		io.Copy(&logWriter{taskID: taskID}, stderr)
	}()

	return cmd.Wait()
}

func runShellCommand(ctx context.Context, dir string, taskID string, cmdStr string) error {
	var cmd *exec.Cmd
	if os.PathSeparator == '\\' {
		cmd = exec.CommandContext(ctx, "cmd", "/C", cmdStr)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", cmdStr)
	}
	cmd.Dir = dir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	go func() {
		io.Copy(&logWriter{taskID: taskID}, stdout)
	}()
	go func() {
		io.Copy(&logWriter{taskID: taskID}, stderr)
	}()

	return cmd.Wait()
}

type logWriter struct {
	taskID string
}

func (lw *logWriter) Write(p []byte) (n int, err error) {
	if lw.taskID != "" {
		sendLog(lw.taskID, string(bytes.TrimSpace(p)), "")
	}
	return len(p), nil
}

func sendLog(taskID, log, status string) {
	reqBody := LogRequest{Log: log, Status: status}
	jsonData, _ := json.Marshal(reqBody)
	http.Post(fmt.Sprintf("%s/api/tasks/%s/logs", nodeJSURL, taskID), "application/json", bytes.NewBuffer(jsonData))
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, _ := filepath.Rel(src, path)
		dstPath := filepath.Join(dst, relPath)
		if info.IsDir() {
			return os.MkdirAll(dstPath, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dstPath, data, info.Mode())
	})
}
