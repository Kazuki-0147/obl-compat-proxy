# OBL Compat Proxy

一个从零开始的 Go 反向代理，对外兼容：

- `POST /v1/chat/completions`
- `POST /v1/messages`

对内统一转发到 OBL 上游：

- `POST /api/v1/chat/completions`

当前只暴露 6 个模型：

- `anthropic/claude-opus-4.6`
- `anthropic/claude-sonnet-4.6`
- `openai/gpt-5.4`
- `openai/gpt-5.4-pro`
- `openai/gpt-5.3-codex`
- `google/gemini-3.1-pro-preview`

支持范围：

- 文本
- 图片输入
- stream / non-stream
- tools
- thinking / reasoning

## 启动

要求：

- Go 1.22 或更高版本
- 一组可用的 OBL 凭据

最简单的启动方式是用 `.env`：

1. 复制环境变量模板
2. 填入你自己的密钥
3. 启动代理

```bash
cd /root/obl-compat-proxy
cp .env.example .env
# 编辑 .env，至少填写:
#   PROXY_API_KEY
#   OBL_REFRESH_TOKEN
#   OBL_ORGANIZATION_ID
# 如果你已经有 access token，也可以填写 OBL_ACCESS_TOKEN
set -a
source .env
set +a
go run ./cmd/proxy
```

也可以不使用 `.env`，直接导出环境变量启动：

```bash
cd /root/obl-compat-proxy
export PROXY_API_KEY=change-me
export OBL_REFRESH_TOKEN=fill-me
export OBL_ORGANIZATION_ID=org_fill_me
go run ./cmd/proxy
```

默认建议监听：

```text
0.0.0.0:18080
```

如果你只想在本机访问，可以改成：

```bash
export LISTEN_ADDR=127.0.0.1:18080
```

启动后先做健康检查：

```bash
curl http://127.0.0.1:18080/healthz
```

再验证模型列表：

```bash
curl -H 'Authorization: Bearer change-me' \
  http://127.0.0.1:18080/v1/models
```

如果你对外开放公网访问，还需要额外放行服务器的入站端口，例如 `18080/tcp`。

## 配置

必须项：

- `PROXY_API_KEY`
- `OBL_REFRESH_TOKEN`
- `OBL_ORGANIZATION_ID`

常用项：

- `LISTEN_ADDR`
- `OBL_API_BASE_URL`
- `OBL_ACCESS_TOKEN`
- `OBL_TOKEN_REFRESH_URL`
- `OBL_CLIENT_ID`
- `REQUEST_BODY_MAX_MB`
- `IMAGE_DATA_URL_MAX_BYTES`
- `MODEL_MAP_JSON`

默认 OBL API 地址：

```text
https://dashboard.openblocklabs.com/api/v1
```

当前 `.env.example` 已经预填了这两个固定值：

```text
OBL_TOKEN_REFRESH_URL=https://api.workos.com/user_management/authenticate
OBL_CLIENT_ID=client_01K8YDZSSKDMK8GYTEHBAW4N4S
```

按当前实测链路，`client_secret` 不需要。

如果你不依赖本机 `ob1`，当前推荐配置是：

- 必填：`PROXY_API_KEY`、`OBL_REFRESH_TOKEN`、`OBL_ORGANIZATION_ID`
- 可选：`OBL_ACCESS_TOKEN`

代理现在支持 `OBL_ACCESS_TOKEN` 留空，只靠 `refresh token` 启动并换取新的 access token。

如果你仍然想依赖本机 `ob1`，程序也可以从：

```text
~/.ob1/credentials.json
```

读取现有 OB1 凭据。

## 鉴权

客户端访问代理时：

```text
Authorization: Bearer <proxy_api_key>
```

或：

```text
x-api-key: <proxy_api_key>
```

代理访问 OBL 时使用：

```text
Authorization: Bearer <access_token>:<organization_id>
```

## thinking 映射

OpenAI / Anthropic 请求会先归一化成内部 thinking 结构，再映射到 OBL 上游字段：

- `reasoning_enabled`
- `reasoning_effort`
- `reasoning_budget`

核心规则：

- `off` -> `reasoning_enabled=false`
- effort 模式 -> `reasoning_enabled=true` + `reasoning_effort`
- budget 模式 -> `reasoning_enabled=true` + `reasoning_budget`
- `xhigh/max` 不直接发成 effort 字符串，而是转 budget；若模型不支持 budget，则降成 `high`

## 测试

```bash
cd /root/obl-compat-proxy
go test ./...
```
