package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

const (
	port           = 8080
	nodeJSURL      = "http://localhost:3000"
	redisHost      = "localhost"
	redisPort      = 6379
	redisPassword  = ""
	redisDB        = 0

	redisQueueMain      = "ci:tasks:main"
	redisQueueProcessing = "ci:tasks:processing"
	redisQueueDetails   = "ci:tasks:main:details:"

	workDir    = "./workspace"
	cacheDir   = "./cache"
	pluginsDir = "../python-plugins"

	pollTimeout    = 5 * time.Second
	workerCount    = 5
	maxRetries     = 3
	taskTimeoutSec = 3600
)

type BuildDetails struct {
	Repo     string   `json:"repo"`
	Branch   string   `json:"branch"`
	Commands []string `json:"commands"`
	Cache    []string `json:"cache"`
	Timeout  int      `json:"timeout"`
	Plugins  []string `json:"plugins"`
}

type TaskInfo struct {
	TaskID   string `json:"taskId"`
	Repo     string `json:"repo"`
	Branch   string `json:"branch"`
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
	redisClient *redis.Client
	runningWorkers int64
	shutdownChan chan struct{}
	wg sync.WaitGroup
)

func main() {
	redisClient = redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", redisHost, redisPort),
		Password: redisPassword,
		DB:       redisDB,
	})

	ctx := context.Background()
	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	log.Println("Connected to Redis")

	if err := os.MkdirAll(workDir, 0755); err != nil {
		log.Fatalf("Failed to create work dir: %v", err)
	}
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		log.Fatalf("Failed to create cache dir: %v", err)
	}

	shutdownChan = make(chan struct{})

	r := gin.Default()
	r.POST("/build", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "Tasks are now pulled via Redis queue"})
	})

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":    "healthy",
			"workers":   atomic.LoadInt64(&runningWorkers),
		})
	})

	go startScheduler(ctx)
	go periodicHealthCheck(ctx)

	log.Printf("Go Builder running on port %d with %d workers", port, workerCount)
	r.Run(fmt.Sprintf(":%d", port))
}

func startScheduler(ctx context.Context) {
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go worker(ctx, i)
	}
}

func worker(ctx context.Context, id int) {
	defer wg.Done()

	log.Printf("Worker %d started", id)
	atomic.AddInt64(&runningWorkers, 1)
	defer atomic.AddInt64(&runningWorkers, -1)

	for {
		select {
		case <-shutdownChan:
			log.Printf("Worker %d shutting down", id)
			return
		case <-ctx.Done():
			log.Printf("Worker %d context cancelled", id)
			return
		default:
			if err := pollAndProcessTask(ctx); err != nil {
				if err != redis.Nil {
					log.Printf("Worker %d error: %v", id, err)
				}
				time.Sleep(time.Second)
			}
		}
	}
}

func pollAndProcessTask(ctx context.Context) error {
	result, err := redisClient.BRPopLPush(ctx, redisQueueMain, redisQueueProcessing, pollTimeout).Result()
	if err != nil {
		return err
	}

	taskID := strings.TrimSpace(result)
	if taskID == "" {
		return nil
	}

	log.Printf("Worker picked up task: %s", taskID)

	detailsKey := redisQueueDetails + taskID
	detailsJSON, err := redisClient.LPop(ctx, detailsKey).Result()
	if err != nil && err != redis.Nil {
		log.Printf("Failed to get task details for %s: %v", taskID, err)
		redisClient.LPush(ctx, redisQueueMain, taskID)
		return err
	}

	var details BuildDetails
	if detailsJSON != "" {
		if err := json.Unmarshal([]byte(detailsJSON), &details); err != nil {
			log.Printf("Failed to parse task details for %s: %v", taskID, err)
			redisClient.LPush(ctx, redisQueueMain, taskID)
			return err
		}
	}

	task := TaskInfo{
		TaskID:   taskID,
		Repo:     details.Repo,
		Branch:   details.Branch,
		Commands: details.Commands,
		Cache:    details.Cache,
		Timeout:  details.Timeout,
		Plugins:  details.Plugins,
	}

	if err := executeBuild(ctx, task); err != nil {
		log.Printf("Task %s failed: %v", taskID, err)
		handleTaskFailure(ctx, task)
	} else {
		log.Printf("Task %s completed successfully", taskID)
	}

	return nil
}

func executeBuild(ctx context.Context, task TaskInfo) error {
	if err := updateTaskStatus(ctx, task.TaskID, "running", ""); err != nil {
		log.Printf("Failed to update task status: %v", err)
	}

	taskDir := filepath.Join(workDir, task.TaskID)
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		return fmt.Errorf("failed to create task dir: %w", err)
	}
	defer os.RemoveAll(taskDir)

	sendLog(task.TaskID, fmt.Sprintf("Starting build... (timeout: %ds)", task.Timeout))

	repoURL := fmt.Sprintf("https://github.com/%s.git", task.Repo)
	if strings.Contains(task.Repo, "gitlab.com") {
		repoURL = fmt.Sprintf("https://gitlab.com/%s.git", task.Repo)
	}

	sendLog(task.TaskID, fmt.Sprintf("Cloning repository: %s", repoURL))

	buildCtx, cancel := context.WithTimeout(ctx, time.Duration(task.Timeout)*time.Second)
	defer cancel()

	if err := runGitClone(buildCtx, taskDir, repoURL, task.Branch); err != nil {
		updateTaskStatus(ctx, task.TaskID, "failed", fmt.Sprintf("Clone failed: %v", err))
		return err
	}

	for _, cachePath := range task.Cache {
		src := filepath.Join(cacheDir, task.Repo, cachePath)
		dst := filepath.Join(taskDir, cachePath)
		if _, err := os.Stat(src); err == nil {
			sendLog(task.TaskID, fmt.Sprintf("Restoring cache: %s", cachePath))
			copyDir(src, dst)
		}
	}

	for _, cmd := range task.Commands {
		sendLog(task.TaskID, fmt.Sprintf("Executing: %s", cmd))
		if err := runShellCommand(buildCtx, taskDir, cmd); err != nil {
			updateTaskStatus(ctx, task.TaskID, "failed", fmt.Sprintf("Command failed: %v", err))
			return err
		}
	}

	for _, plugin := range task.Plugins {
		sendLog(task.TaskID, fmt.Sprintf("Running plugin: %s", plugin))
		pluginPath := filepath.Join(pluginsDir, plugin+".py")
		if _, err := os.Stat(pluginPath); err == nil {
			if err := runPythonPlugin(buildCtx, taskDir, pluginPath); err != nil {
				updateTaskStatus(ctx, task.TaskID, "failed", fmt.Sprintf("Plugin failed: %v", err))
				return err
			}
		} else {
			sendLog(task.TaskID, fmt.Sprintf("Plugin not found: %s", plugin))
		}
	}

	for _, cachePath := range task.Cache {
		src := filepath.Join(taskDir, cachePath)
		dst := filepath.Join(cacheDir, task.Repo, cachePath)
		if _, err := os.Stat(src); err == nil {
			sendLog(task.TaskID, fmt.Sprintf("Saving cache: %s", cachePath))
			os.MkdirAll(filepath.Dir(dst), 0755)
			copyDir(src, dst)
		}
	}

	updateTaskStatus(ctx, task.TaskID, "success", "Build completed successfully!")
	sendLog(task.TaskID, "Build completed successfully!")
	return nil
}

func handleTaskFailure(ctx context.Context, task TaskInfo) {
	retryKey := fmt.Sprintf("task:%s:retry", task.TaskID)
	retryCount, _ := redisClient.Incr(ctx, retryKey).Result()
	redisClient.Expire(ctx, retryKey, 24*time.Hour)

	if retryCount < maxRetries {
		log.Printf("Task %s will be retried (attempt %d/%d)", task.TaskID, retryCount, maxRetries)
		redisClient.LPush(ctx, redisQueueMain, task.TaskID)
	} else {
		log.Printf("Task %s exceeded max retries, marking as failed permanently", task.TaskID)
		updateTaskStatus(ctx, task.TaskID, "failed", fmt.Sprintf("Exceeded max retries (%d)", maxRetries))
	}
}

func updateTaskStatus(ctx context.Context, taskID, status, logMsg string) error {
	taskKey := fmt.Sprintf("task:%s", taskID)
	multi := redisClient.Multi()

	multi.HSet(ctx, taskKey, map[string]interface{}{
		"status": status,
	})
	multi.HSet(ctx, taskKey, map[string]interface{}{
		"finishedAt": time.Now().Format(time.RFC3339),
	})

	if status == "running" {
		multi.HSet(ctx, taskKey, map[string]interface{}{
			"startedAt": time.Now().Format(time.RFC3339),
		})
	}

	_, err := multi.Exec(ctx)
	if err != nil {
		return err
	}

	reqBody := LogRequest{Status: status}
	if logMsg != "" {
		reqBody.Log = logMsg
	}
	jsonData, _ := json.Marshal(reqBody)
	http.Post(fmt.Sprintf("%s/api/tasks/%s/logs", nodeJSURL, taskID), "application/json", bytes.NewBuffer(jsonData))

	return nil
}

func sendLog(taskID, message string) {
	reqBody := LogRequest{Log: message}
	jsonData, _ := json.Marshal(reqBody)
	http.Post(fmt.Sprintf("%s/api/tasks/%s/logs", nodeJSURL, taskID), "application/json", bytes.NewBuffer(jsonData))
}

func runGitClone(ctx context.Context, dir, url, branch string) error {
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--branch", branch, url, ".")
	cmd.Dir = dir
	cmd.Stdout = &logWriter{taskID: ""}
	cmd.Stderr = &logWriter{taskID: ""}
	return cmd.Run()
}

func runShellCommand(ctx context.Context, dir, cmdStr string) error {
	var cmd *exec.Cmd
	if filepath.Separator == '\\' {
		cmd = exec.CommandContext(ctx, "cmd", "/C", cmdStr)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", cmdStr)
	}
	cmd.Dir = dir
	cmd.Stdout = &logWriter{taskID: ""}
	cmd.Stderr = &logWriter{taskID: ""}
	return cmd.Run()
}

func runPythonPlugin(ctx context.Context, dir, pluginPath string) error {
	cmd := exec.CommandContext(ctx, "python", pluginPath)
	cmd.Dir = dir
	cmd.Stdout = &logWriter{taskID: ""}
	cmd.Stderr = &logWriter{taskID: ""}
	return cmd.Run()
}

type logWriter struct {
	taskID string
}

func (lw *logWriter) Write(p []byte) (n int, err error) {
	if len(p) > 0 && lw.taskID != "" {
		msg := string(bytes.TrimSpace(p))
		if msg != "" {
			sendLog(lw.taskID, msg)
		}
	}
	return len(p), nil
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

func periodicHealthCheck(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-shutdownChan:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			processLen, _ := redisClient.LLen(ctx, redisQueueProcessing).Result()
			mainLen, _ := redisClient.LLen(ctx, redisQueueMain).Result()
			log.Printf("Health check - Processing: %d, Main queue: %d, Workers: %d",
				processLen, mainLen, atomic.LoadInt64(&runningWorkers))
		}
	}
}
