# Node.js-Go-Python-CI-CD-
前端（Node.js 提供 HTML 或独立前端框架）展示流水线配置、触发按钮、构建日志。Node.js 作为主服务，存储配置和任务记录。Go 服务作为构建执行器，拉取代码、执行用户定义的命令（支持缓存、超时），实时流式输出日志到 Node.js。Python 可作为插件脚本（例如单元测试、依赖扫描），由 Go 调用。实现 GitHub/GitLab Webhook 自动触发。输出 Node.js、Go、Python 三端代码。
