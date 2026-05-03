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
│  │  • 任务记录存储                                    │ │
│  │  • GitHub/GitLab Webhook 接收                      │ │
│  └───────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────┐
│                  Go 构建执行器 (8080)                   │
│  ┌───────────────────────────────────────────────────┐ │
│  │  • Git 仓库拉取                                    │ │
│  │  • 用户命令执行                                    │ │
│  │  • 构建缓存支持                                    │ │
│  │  • 超时控制                                        │ │
│  │  • Python 插件调用                                 │ │
│  │  • 实时日志流传输                                  │ │
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

## 快速开始

### 前置要求

- Node.js 16+
- Go 1.21+
- Python 3.7+
- Git

### 启动

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

**1. 启动 Node.js 服务:**
```bash
cd nodejs-service
npm install
npm start
```

**2. 启动 Go 构建器 (新开终端):**
```bash
cd go-builder
go mod tidy
go run main.go
```

### 访问

打开浏览器访问: http://localhost:3000

## 使用说明

### 1. 创建流水线

点击 "新建流水线" 按钮，填写：
- 流水线名称
- Git 仓库（格式：`owner/repo` 或 `gitlab.com/owner/repo`）
- 分支（默认 `main`，`*` 表示所有分支）
- 构建命令（每行一个）
- 缓存目录（每行一个，可选）
- 超时时间（秒，默认 600）
- 插件（每行一个，可选）

### 2. 手动触发

点击流水线卡片上的 "触发构建" 按钮即可。

### 3. Webhook 配置

**GitHub:**
- 仓库设置 → Webhooks → Add webhook
- Payload URL: `http://your-server:3000/webhook/github`
- Content type: `application/json`
- 事件选择: Just the push event

**GitLab:**
- 仓库设置 → Integrations
- URL: `http://your-server:3000/webhook/gitlab`
- 触发事件: Push events

### 4. 查看日志

点击任务记录卡片即可查看实时构建日志。

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

## API 接口

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /api/pipelines | 获取所有流水线 |
| POST | /api/pipelines | 创建流水线 |
| DELETE | /api/pipelines/:id | 删除流水线 |
| POST | /api/pipelines/:id/trigger | 触发流水线构建 |
| GET | /api/tasks | 获取所有任务 |
| POST | /api/tasks/:id/logs | 发送构建日志 |
| POST | /webhook/github | GitHub Webhook |
| POST | /webhook/gitlab | GitLab Webhook |

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
