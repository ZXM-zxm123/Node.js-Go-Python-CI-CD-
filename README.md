# CI/CD System (Node.js + Go + Python)

一个简单但完整的 CI/CD 系统，使用 Node.js 作为主服务，Go 作为构建执行器，Python 作为插件脚本。

## 架构

```
┌─────────────────────────────────────────────────────────┐
│                     浏览器前端                            │
│              (流水线配置、触发按钮、日志查看)              │
└─────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────┐
│                   Node.js 主服务 (3000)                  │
│  ┌───────────────────────────────────────────────────┐ │
│  │  • Express Web 服务器                             │ │
│  │  • Socket.IO 实时通信                              │ │
│  │  • 流水线配置管理                                  │ │
│  │  • 任务记录存储 → Redis                            │ │
│  │  • GitHub/GitLab Webhook 接收                      │ │
│  │  • 背压控制 (队列长度限制)                          │ │
│  │  • 超时巡检机制                                    │ │
│  └───────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────┐
│                      Redis (6379)                       │
│  ┌───────────────────────────────────────────────────┐ │
│  │  • ci:tasks:main (List) - 主任务队列              │ │
│  │  • ci:tasks:processing (List) - 处理中队列         │ │
│  │  • ci:tasks:main:details:* (List) - 任务详情      │ │
│  │  • task:* (Hash) - 任务状态                        │ │
│  └───────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────┐
│                  Go 构建执行器 (8080)                   │
│  ┌───────────────────────────────────────────────────┐ │
│  │  • 多 Worker 并发处理 (默认 5 个)                   │ │
│  │  • BRPOPLPUSH 事务性拉取                           │ │
│  │  • Git 仓库拉取                                    │ │
│  │  • 用户命令执行                                    │ │
│  │  • 构建缓存支持                                    │ │
│  │  • 超时控制                                        │ │
│  │  • Python 插件调用                                 │ │
│  │  • 实时日志流传输                                  │ │
│  │  • 自动重试机制                                    │ │
│  │  • 健康检查                                        │ │
│  └───────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────┐
│                    Python 插件                           │
│  ┌───────────────────────────────────────────────────┐ │
│  │  • unittest.py - 单元测试                          │ │
│  │  • dependency-scan.py - 依赖扫描                    │ │
│  └───────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────┘
```

## 核心修复：任务调度可靠性

### 问题
同时触发大量任务时，部分任务未被调度（丢失）。

### 原因
原设计使用内存 channel，Go 服务通过 HTTP 调用获取任务，存在以下问题：
1. 内存 channel 在 Go 重启时会丢失
2. 没有事务性拉取，并发时会出现竞争
3. 缺少重试和巡检机制

### 解决方案
使用 Redis 作为持久化存储和消息队列：

1. **立即持久化**：Node.js 接收任务后立即写入 Redis（LPUSH），状态为 pending
2. **事务性拉取**：Go 使用 `BRPOPLPUSH` 从主队列移到处理队列，确保不丢失
3. **超时重试**：任务超时后自动移回主队列重试（默认 3 次）
4. **定期巡检**：Node.js 每 30 秒检查处理中的任务，超时则重试
5. **背压控制**：队列超过阈值（80%）拒绝新任务

## 功能特性

- ✅ 流水线配置管理（创建、删除）
- ✅ 手动触发构建
- ✅ GitHub/GitLab Webhook 自动触发
- ✅ Git 仓库拉取（支持 GitHub/GitLab）
- ✅ 用户自定义构建命令
- ✅ 构建缓存支持
- ✅ 超时控制
- ✅ 实时日志流
- ✅ Python 插件系统
- ✅ Web 前端界面
- ✅ **Redis 持久化存储**
- ✅ **事务性任务拉取**
- ✅ **超时自动重试**
- ✅ **背压控制**
- ✅ **多 Worker 并发处理**

## 前置要求

- Node.js 16+
- Go 1.21+
- Python 3.7+
- Git
- Redis 6+

## 启动

**Windows:**
```cmd
start.bat
```

**Linux/Mac:**
```bash
chmod +x start.sh
./start.sh
```

### 手动启动

**1. 启动 Redis:**
```bash
redis-server
```

**2. 启动 Node.js 服务:**
```bash
cd nodejs-service
npm install
npm start
```

**3. 启动 Go 构建器 (新开终端):**
```bash
cd go-builder
go mod tidy
go run main.go
```

### 访问

打开浏览器访问: http://localhost:3000

## 配置说明

### 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| REDIS_HOST | localhost | Redis 主机 |
| REDIS_PORT | 6379 | Redis 端口 |

### Go Builder 参数

```go
workerCount    = 5     // 并发 Worker 数量
maxRetries     = 3     // 最大重试次数
taskTimeoutSec = 3600  // 任务超时（秒）
pollTimeout    = 5s    // 队列轮询超时
```

### 背压控制

```go
MAX_QUEUE_SIZE = 10000  // 最大队列长度
```

当队列长度超过 80% 时，Webhook 会拒绝新任务。

## API 接口

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /api/pipelines | 获取所有流水线 |
| POST | /api/pipelines | 创建流水线 |
| DELETE | /api/pipelines/:id | 删除流水线 |
| POST | /api/pipelines/:id/trigger | 触发流水线构建 |
| GET | /api/tasks | 获取所有任务 |
| POST | /api/tasks/:id/logs | 发送构建日志 |
| GET | /api/queue/status | 获取队列状态 |
| POST | /webhook/github | GitHub Webhook |
| POST | /webhook/gitlab | GitLab Webhook |

## Redis 数据结构

### 主队列 `ci:tasks:main`
```
LPUSH taskId  // 添加任务
BRPOPLPUSH ci:tasks:main ci:tasks:processing 0  // 原子性拉取
```

### 处理队列 `ci:tasks:processing`
```
任务详情队列 (atomic push from main)
```

### 任务详情 `ci:tasks:main:details:{taskId}`
```
LPUSH '{"repo":"...","branch":"main",...}'
LPOP // 消费
```

### 任务状态 `task:{taskId}`
```
HSET status "pending|running|success|failed"
HSET startedAt "2024-01-01T00:00:00Z"
HSET finishedAt "2024-01-01T00:05:00Z"
HSET retryCount 0
```

## 目录结构

```
.
├── nodejs-service/         # Node.js 主服务
│   ├── server.js          # 服务主文件
│   ├── package.json       # 依赖配置
│   └── public/            # 前端文件
│       ├── index.html
│       ├── style.css
│       └── app.js
├── go-builder/            # Go 构建执行器
│   ├── main.go            # 服务主文件
│   └── go.mod             # Go 模块
├── python-plugins/        # Python 插件
│   ├── unittest.py        # 单元测试插件
│   └── dependency-scan.py # 依赖扫描插件
├── data/                  # 数据存储目录
├── start.bat              # Windows 启动脚本
├── start.sh               # Linux/Mac 启动脚本
└── README.md              # 文档
```

## 开发插件

在 `python-plugins/` 目录下创建 `.py` 文件，插件会在构建过程中被调用。插件输出会被记录到构建日志中，非零退出码会导致构建失败。

插件示例:
```python
#!/usr/bin/env python3
import sys

print("My custom plugin running...")
# 你的逻辑
sys.exit(0)  # 0 表示成功
```

## 许可证

MIT
