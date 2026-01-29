# PingGo 🚀

PingGo 是一个为了解决社区中缺乏 **轻量、纯 Go 编写、单可执行文件、美观现代** 的监控 WebApp 而创造的项目。

它旨在为个人开发者和中小型团队提供一个开箱即用的网站/服务监控方案，无需复杂的依赖安装，仅需一个二进制文件即可运行。

## ✨ 特性

-   **🚀 极简部署**：纯 Go 编写，静态资源内嵌，编译后仅为一个可执行文件。
-   **🎨 现代 UI**：基于 Tailwind CSS + Alpine.js + Chart.js 构建，界面美观、响应式。
-   **📊 数据分层存储**：内置降采样算法，自动将原始心跳数据聚合成小时/天维度，在保留长期趋势的同时极大地节省磁盘空间。
-   **📧 智能通知**：集成 Resend API，支持邮件通知，配置简单，发送稳定。
-   **💾 本地存储**：使用 SQLite 存储数据，性能优秀且无需额外数据库服务。
-   **⚙️ 灵活配置**：支持配置文件与环境变量双重配置方式。

## 🛠️ 快速开始

### 编译构建

```bash
# 编译
go build -o pinggo main.go

# 运行
./pinggo
```

## 📝 配置说明

你可以通过修改 `config.yaml` 或设置环境变量来配置程序。

### 配置文件 (config.yaml)

参考 `config.yaml.example`：

```yaml
server:
  port: 37374
notification:
  resend_api_key: "你的_RESEND_API_KEY"
  email: "接收通知的邮箱@example.com"
  from_email: "发件人邮箱@yourdomain.com" # 可选
  from_name: "PingGo 监控" # 可选

# 数据保留配置 - 分层存储策略
retention:
  raw_hours: 24      # 原始心跳数据保留 24 小时
  hourly_days: 7     # 小时级聚合数据保留 7 天
  daily_days: 365    # 日级聚合数据保留 1 年
```

### 环境变量

支持以下环境变量覆盖：
- `RESEND_API_KEY`: Resend API 密钥
- `NOTIFICATION_EMAIL`: 接收通知的邮箱
- `PORT`: 服务监听端口

## 📂 项目结构

- `main.go`: 程序入口
- `server/`: 基于标准库的 Web 服务实现
- `monitor/`: 核心监控逻辑
- `db/`: 数据持久化与分层存储策略
- `dist/`: 前端静态资源 (编译时自动内嵌)
- `config/`: 配置管理

## 🤝 贡献

欢迎提交 Issue 或 Pull Request！

## 📄 开源协议

MIT License
