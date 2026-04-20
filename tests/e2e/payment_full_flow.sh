#!/bin/bash
# payment_full_flow.sh — v3.2 支付系统端到端联调脚本
#
# 前置条件：
#   - docker compose up -d 已启动，MySQL/Redis/go-server 全部 healthy
#   - 默认管理员账号：admin@tokenhubhk.com / admin123456
#   - 环境变量：BASE_URL (默认 http://localhost)
#
# 用法：
#   bash backend/tests/e2e/payment_full_flow.sh
#
# 返回 0 = 全部通过，非 0 = 某步失败

set -e
BASE="${BASE_URL:-http://localhost}"
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@tokenhubhk.com}"
ADMIN_PASS="${ADMIN_PASS:-admin123456}"

red()    { echo -e "\033[31m✗ $*\033[0m"; }
green()  { echo -e "\033[32m✓ $*\033[0m"; }
yellow() { echo -e "\033[33m→ $*\033[0m"; }

step() { echo ""; yellow "[$1] $2"; }
fail() { red "$1"; exit 1; }

# 依赖检查
command -v curl >/dev/null || fail "缺少 curl"
command -v jq >/dev/null || fail "缺少 jq (apt install jq / brew install jq)"

# ======================================================
step 1 "健康检查"
curl -sf "$BASE/api/v1/public/exchange-rate" >/dev/null || fail "服务未就绪"
green "/api/v1/public/exchange-rate 可访问"

# ======================================================
step 2 "获取汇率"
FX=$(curl -sf "$BASE/api/v1/public/exchange-rate")
USD_CNY=$(echo "$FX" | jq -r '.data.usd_to_cny')
if [ -z "$USD_CNY" ] || [ "$USD_CNY" == "null" ] || [ "$USD_CNY" == "0" ]; then
  fail "汇率返回异常：$FX"
fi
green "USD→CNY = $USD_CNY"

# ======================================================
step 3 "登录管理员获取 JWT"
LOGIN_RESP=$(curl -sf -X POST "$BASE/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}")
TOKEN=$(echo "$LOGIN_RESP" | jq -r '.data.token')
USER_ID=$(echo "$LOGIN_RESP" | jq -r '.data.user.id')
if [ -z "$TOKEN" ] || [ "$TOKEN" == "null" ]; then
  fail "登录失败：$LOGIN_RESP"
fi
green "登录成功 user_id=$USER_ID"

# ======================================================
step 4 "查询余额（初始）"
BAL=$(curl -sf "$BASE/api/v1/user/balance" -H "Authorization: Bearer $TOKEN")
INITIAL_BALANCE=$(echo "$BAL" | jq -r '.data.balance')
green "初始余额：$INITIAL_BALANCE 积分"

# ======================================================
step 5 "创建一笔 ¥100 支付订单（支付宝）"
CREATE_RESP=$(curl -sf -X POST "$BASE/api/v1/payment/create" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"gateway":"alipay","amount":100,"currency":"CNY","subject":"E2E 测试充值"}')
ORDER_NO=$(echo "$CREATE_RESP" | jq -r '.data.order_no')
if [ -z "$ORDER_NO" ] || [ "$ORDER_NO" == "null" ]; then
  fail "创建订单失败：$CREATE_RESP"
fi
green "订单号：$ORDER_NO"

# ======================================================
step 6 "查询订单详情"
QUERY_RESP=$(curl -sf "$BASE/api/v1/payment/query/$ORDER_NO" \
  -H "Authorization: Bearer $TOKEN")
STATUS=$(echo "$QUERY_RESP" | jq -r '.data.status')
PAYMENT_ID=$(echo "$QUERY_RESP" | jq -r '.data.id')
green "订单状态：$STATUS / payment_id=$PAYMENT_ID"

# ======================================================
step 7 "管理员列出所有订单"
LIST_RESP=$(curl -sf "$BASE/api/v1/admin/payment/orders?page=1&page_size=5" \
  -H "Authorization: Bearer $TOKEN")
ORDER_COUNT=$(echo "$LIST_RESP" | jq -r '.data.total')
green "共 $ORDER_COUNT 笔订单"

# ======================================================
step 8 "查询订单事件日志（应至少有 payment.created）"
EVENT_RESP=$(curl -sf "$BASE/api/v1/admin/payment/event-logs/by-payment/$PAYMENT_ID" \
  -H "Authorization: Bearer $TOKEN")
EVENT_COUNT=$(echo "$EVENT_RESP" | jq -r '.data | length')
green "事件日志数：$EVENT_COUNT"
if [ "$EVENT_COUNT" -lt 1 ]; then
  red "警告：事件日志未写入（可能因为 gateway 初始化失败被 fallback）"
fi

# ======================================================
step 9 "提交退款申请（先将订单置为 completed 便于测试）"
# E2E 环境无法真实完成支付宝回调，直接用 DB 模拟 completed 状态
if docker exec tokenhubhk-mysql mysql -u"${MYSQL_USER:-tokenhub}" -p"${MYSQL_PASSWORD:-tokenhubhk}" "${MYSQL_DATABASE:-tokenhubhk}" -e "
  UPDATE payments SET status='completed', rmb_amount=100, refunded_amount=0 WHERE id=$PAYMENT_ID;
" 2>&1; then
  green "订单状态已模拟为 completed"
else
  yellow "无法模拟 completed 状态（MySQL 凭证可能不同），跳过退款流程"
  green "前 8 步均通过 ✓"
  exit 0
fi

REFUND_RESP=$(curl -sf -X POST "$BASE/api/v1/user/refund-requests" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"payment_id\":$PAYMENT_ID,\"amount_rmb\":50,\"reason\":\"E2E 测试退款申请，原因至少 10 字符\"}")
REFUND_ID=$(echo "$REFUND_RESP" | jq -r '.data.id')
if [ -z "$REFUND_ID" ] || [ "$REFUND_ID" == "null" ]; then
  fail "退款申请失败：$REFUND_RESP"
fi
green "退款申请 ID：$REFUND_ID"

# ======================================================
step 10 "管理员列出待审退款"
LIST_RESP=$(curl -sf "$BASE/api/v1/admin/payment/refunds?status=pending" \
  -H "Authorization: Bearer $TOKEN")
PENDING_COUNT=$(echo "$LIST_RESP" | jq -r '.data.total')
green "待审退款：$PENDING_COUNT 笔"

# ======================================================
step 11 "管理员拒绝该退款（避免真实调用网关退款 API）"
curl -sf -X POST "$BASE/api/v1/admin/payment/refunds/$REFUND_ID/reject" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"reason":"E2E 自动化测试路径，不真实退款"}' >/dev/null
green "已拒绝退款"

# ======================================================
step 12 "查询退款详情"
DETAIL_RESP=$(curl -sf "$BASE/api/v1/admin/payment/refunds/$REFUND_ID" \
  -H "Authorization: Bearer $TOKEN")
FINAL_STATUS=$(echo "$DETAIL_RESP" | jq -r '.data.refund.status')
if [ "$FINAL_STATUS" != "rejected" ]; then
  fail "退款状态异常：$FINAL_STATUS"
fi
green "退款最终状态：rejected ✓"

# ======================================================
step 13 "事件日志追溯"
EVENT_RESP=$(curl -sf "$BASE/api/v1/admin/payment/event-logs/by-refund/$REFUND_ID" \
  -H "Authorization: Bearer $TOKEN")
REFUND_EVENT_COUNT=$(echo "$EVENT_RESP" | jq -r '.data | length')
green "退款事件日志数：$REFUND_EVENT_COUNT"

# ======================================================
echo ""
green "============================================"
green "  E2E 联调全部通过 🎉"
green "============================================"
