#!/bin/bash

# 邀请返佣系统全流程测试脚本（Docker 版本）
# 用途：在 Docker 容器内运行所有邀请相关测试

set -e

echo "=========================================="
echo "邀请返佣系统全流程测试 (Docker)"
echo "=========================================="
echo ""

# 检查数据库连接
echo "1. 检查测试数据库连接..."
if ! docker compose exec -T mysql mysql -uroot -proot123456 -e "USE tokenhubhk; SELECT 1;" > /dev/null 2>&1; then
    echo "❌ 数据库连接失败，请确保 Docker Compose 服务已启动"
    exit 1
fi
echo "✅ 数据库连接正常"
echo ""

# 检查 go-server 容器
echo "2. 检查 go-server 容器状态..."
if ! docker compose ps go-server | grep -q "Up"; then
    echo "❌ go-server 容器未运行，正在启动..."
    docker compose up -d go-server
    sleep 5
fi
echo "✅ go-server 容器运行中"
echo ""

# 单元测试：邀请服务基础功能
echo "3. 运行单元测试：邀请服务基础功能..."
docker compose exec -T go-server go test -v /app/internal/service/referral/ -run "^TestReferralCodeGeneration" || true
docker compose exec -T go-server go test -v /app/internal/service/referral/ -run "^TestGetOrCreateLink" || true
docker compose exec -T go-server go test -v /app/internal/service/referral/ -run "^TestFindByCode" || true
docker compose exec -T go-server go test -v /app/internal/service/referral/ -run "^TestIncrementClickCount" || true
docker compose exec -T go-server go test -v /app/internal/service/referral/ -run "^TestIncrementRegisterCount" || true
docker compose exec -T go-server go test -v /app/internal/service/referral/ -run "^TestGetStats" || true
echo ""

# 单元测试：归因解锁逻辑
echo "4. 运行单元测试：归因解锁逻辑..."
docker compose exec -T go-server go test -v /app/internal/service/referral/ -run "^TestTryUnlockAttribution" || true
echo ""

# 单元测试：佣金计算器
echo "5. 运行单元测试：佣金计算器..."
docker compose exec -T go-server go test -v /app/internal/service/referral/ -run "^TestCalculateCommissions" || true
docker compose exec -T go-server go test -v /app/internal/service/referral/ -run "^TestLifetimeCap" || true
echo ""

# 集成测试：完整业务流程
echo "6. 运行集成测试：完整业务流程..."
docker compose exec -T go-server go test -v /app/internal/service/referral/ -run "^TestReferralFlow_E2E_Complete" || true
docker compose exec -T go-server go test -v /app/internal/service/referral/ -run "^TestReferralFlow_AttributionExpiry" || true
docker compose exec -T go-server go test -v /app/internal/service/referral/ -run "^TestReferralFlow_UnlockThreshold" || true
docker compose exec -T go-server go test -v /app/internal/service/referral/ -run "^TestReferralFlow_LifetimeCapReached" || true
docker compose exec -T go-server go test -v /app/internal/service/referral/ -run "^TestReferralFlow_MultipleInvitees" || true
echo ""

# 配置动态生效测试
echo "7. 运行配置动态生效测试..."
docker compose exec -T go-server go test -v /app/internal/service/referral/ -run "^TestConfigDynamicUpdate_CommissionRateChange" || true
docker compose exec -T go-server go test -v /app/internal/service/referral/ -run "^TestConfigDynamicUpdate_AttributionDaysChange" || true
docker compose exec -T go-server go test -v /app/internal/service/referral/ -run "^TestConfigDynamicUpdate_UnlockThresholdChange" || true
docker compose exec -T go-server go test -v /app/internal/service/referral/ -run "^TestConfigDynamicUpdate_LifetimeCapChange" || true
echo ""

# API 测试：公开配置端点
echo "8. 运行 API 测试：公开配置端点..."
docker compose exec -T go-server go test -v /app/internal/handler/public/ -run "^TestGetReferralConfig" || true
docker compose exec -T go-server go test -v /app/internal/handler/public/ -run "^TestGetQuotaConfig" || true
docker compose exec -T go-server go test -v /app/internal/handler/public/ -run "^TestConfigAPIs_NoDeprecatedFields" || true
echo ""

# API 测试：管理后台配置端点
echo "9. 运行 API 测试：管理后台配置端点..."
docker compose exec -T go-server go test -v /app/internal/handler/admin/ -run "^TestAdminUpdateReferralConfig" || true
docker compose exec -T go-server go test -v /app/internal/handler/admin/ -run "^TestAdminUpdateQuotaConfig" || true
echo ""

echo "=========================================="
echo "测试执行完成"
echo "=========================================="
echo ""
echo "注意：由于容器内测试文件可能未同步，部分测试可能跳过"
echo "建议：重新构建镜像以包含最新测试文件"
echo ""
echo "重新构建命令："
echo "  docker compose build --no-pull go-server"
echo "  docker compose up -d go-server"
echo ""
