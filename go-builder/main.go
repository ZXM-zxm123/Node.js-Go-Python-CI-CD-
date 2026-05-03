package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
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

	redisQueueMain       = "ci:tasks:main"
	redisQueueProcessing = "ci:tasks:processing"
	redisQueueDetails    = "ci:tasks:main:details:"

	workDir    = "./workspace"
	cacheDir   = "./cache"
	pluginsDir = "../python-plugins"

	pollTimeout    = 5 * time.Second
	workerCount    = 5
	defaultMaxRetries = 3
	taskTimeoutSec    = 3600

	baseRetryDelay = 10 * time.Second
	maxRetryDelay  = 5 * time.Minute
)

var (
	ErrNetworkFailure   = errors.New("network failure")
	ErrTemporaryFailure = errors.New("temporary failure")
	ErrPermanentFailure = errors.New("permanent failure")
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
	TaskID   string   `json:"taskId"`
	Repo     string   `json:"repo"`
	Branch   string   `json:"branch"`
	Commands []string `json:"commands"`
	Cache    []string `json:"cache"`
	Timeout  int      `json:"timeout"`
	Plugins  []string `json:"plugins"`
}

type LogRequest struct {
	Log        string `json:"log"`
	Status     string `json:"status"`
	RetryCount int    `json:"retryCount,omitempty"`
}

var (
	redisClient    *redis.Client
	runningWorkers int64
	shutdownChan   chan struct{}
	wg             sync.WaitGroup
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
			"status":  "healthy",
			"workers": atomic.LoadInt64(&runningWorkers),
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

	taskKey := fmt.Sprintf("task:%s", taskID)
	retryCountStr, _ := redisClient.HGet(ctx, taskKey, "retryCount").Result()
	retryCount := 0
	if retryCountStr != "" {
		fmt.Sscanf(retryCountStr, "%d", &retryCount)
	}

	detailsKey := redisQueueDetails + taskID
	detailsJSON, err := redisClient.LPop(ctx, detailsKey).Result()
	if err != nil && err != redis.Nil {
		log.Printf("Failed to get task details for %s: %v", taskID, err)
		redisClient.LPush(ctx, redisQueueMain, taskID)
		redisClient.LPush(ctx, detailsKey, "{}")
		return err
	}

	var details BuildDetails
	if detailsJSON != "" {
		if err := json.Unmarshal([]byte(detailsJSON), &details); err != nil {
			log.Printf("Failed to parse task details for %s: %v", taskID, err)
			redisClient.LPush(ctx, redisQueueMain, taskID)
			redisClient.LPush(ctx, detailsKey, "{}")
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

	buildErr := executeBuild(ctx, task)
	if buildErr != nil {
		log.Printf("Task %s failed: %v", taskID, buildErr)
		handleTaskFailure(ctx, task, details, buildErr, retryCount)
	} else {
		log.Printf("Task %s completed successfully", taskID)
		removeFromProcessingQueue(ctx, taskID)
	}

	return nil
}

func removeFromProcessingQueue(ctx context.Context, taskID string) {
	redisClient.LRem(ctx, redisQueueProcessing, 0, taskID)
}

func executeBuild(ctx context.Context, task TaskInfo) error {
	if err := updateTaskStatus(ctx, task.TaskID, "running", ""); err != nil {
		log.Printf("Failed to update task status: %v", err)
	}

	taskDir := filepath.Join(workDir, task.TaskID)
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		return fmt.Errorf("%w: failed to create task dir", ErrPermanentFailure)
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
		if isNetworkError(err) {
			return fmt.Errorf("%w: clone failed: %v", ErrNetworkFailure, err)
		}
		return fmt.Errorf("%w: clone failed: %v", ErrTemporaryFailure, err)
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
			return fmt.Errorf("%w: command '%s' failed: %v", ErrTemporaryFailure, cmd, err)
		}
	}

	for _, plugin := range task.Plugins {
		sendLog(task.TaskID, fmt.Sprintf("Running plugin: %s", plugin))
		pluginPath := filepath.Join(pluginsDir, plugin+".py")
		if _, err := os.Stat(pluginPath); err == nil {
			if err := runPythonPlugin(buildCtx, taskDir, pluginPath); err != nil {
				return fmt.Errorf("%w: plugin '%s' failed: %v", ErrTemporaryFailure, plugin, err)
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
	removeFromProcessingQueue(ctx, task.TaskID)
	return nil
}

func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()

	networkIndicators := []string{
		"connection refused",
		"connection reset",
		"connection timed out",
		"timeout",
		"no such host",
		"network",
		"temporary failure",
		"i/o timeout",
		"use of closed",
		"broken pipe",
	}

	errLower := strings.ToLower(errStr)
	for _, indicator := range networkIndicators {
		if strings.Contains(errLower, indicator) {
			return true
		}
	}
	return false
}

func isPermanentError(err error) bool {
	return errors.Is(err, ErrPermanentFailure)
}

func handleTaskFailure(ctx context.Context, task TaskInfo, details BuildDetails, buildErr error, currentRetryCount int) {
	taskKey := fmt.Sprintf("task:%s", task.TaskID)
	maxRetries := defaultMaxRetries

	newRetryCount := currentRetryCount + 1

	if errors.Is(buildErr, ErrPermanentFailure) {
		log.Printf("Task %s failed with permanent error: %v", task.TaskID, buildErr)
		updateTaskStatusWithRetry(ctx, task.TaskID, "failed", fmt.Sprintf("Build failed: %v", buildErr), newRetryCount)
		removeFromProcessingQueue(ctx, task.TaskID)
		return
	}

	if newRetryCount > maxRetries {
		log.Printf("Task %s exceeded max retries (%d), marking as failed permanently", task.TaskID, maxRetries)
		updateTaskStatusWithRetry(ctx, task.TaskID, "failed", fmt.Sprintf("Exceeded max retries (%d): %v", maxRetries, buildErr), newRetryCount)
		removeFromProcessingQueue(ctx, task.TaskID)
		return
	}

	delay := calculateRetryDelay(newRetryCount)
	log.Printf("Task %s will be retried in %v (attempt %d/%d) - reason: %v",
		task.TaskID, delay, newRetryCount, maxRetries, buildErr)

	sendLog(task.TaskID, fmt.Sprintf("Build failed (attempt %d/%d): %v. Retrying in %v...",
		newRetryCount, maxRetries, buildErr, delay))

	go func() {
		select {
		case <-time.After(delay):
			redisClient.HSet(ctx, taskKey, map[string]interface{}{
				"status":     "pending",
				"retryCount": newRetryCount,
				"startedAt":  "",
				"finishedAt": "",
			})

			detailsJSON, _ := json.Marshal(details)
			redisClient.LPush(ctx, redisQueueMain, task.TaskID)
			redisClient.LPush(ctx, redisQueueDetails+task.TaskID, string(detailsJSON))

			log.Printf("Task %s re-queued for retry %d", task.TaskID, newRetryCount)
		case <-ctx.Done():
			log.Printf("Retry for task %s cancelled due to context", task.TaskID)
		}
	}()
}

func calculateRetryDelay(retryCount int) time.Duration {
	delay := time.Duration(math.Pow(2, float64(retryCount))) * baseRetryDelay
	if delay > maxRetryDelay {
		delay = maxRetryDelay
	}
	jitter := time.Duration(time.Now().UnixNano()%1000) * time.Millisecond
	return delay + jitter
}

func updateTaskStatusWithRetry(ctx context.Context, taskID, status, logMsg string, retryCount int) error {
	taskKey := fmt.Sprintf("task:%s", taskID)

	multi := redisClient.Multi()
	multi.HSet(ctx, taskKey, map[string]interface{}{
		"status":     status,
		"retryCount": retryCount,
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

	reqBody := LogRequest{
		Status:     status,
		Log:        logMsg,
		RetryCount: retryCount,
	}
	jsonData, _ := json.Marshal(reqBody)
	http.Post(fmt.Sprintf("%s/api/tasks/%s/logs", nodeJSURL, taskID), "application/json", bytes.NewBuffer(jsonData))

	return nil
}

func updateTaskStatus(ctx context.Context, taskID, status, logMsg string) error {
	return updateTaskStatusWithRetry(ctx, taskID, status, logMsg, 0)
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
