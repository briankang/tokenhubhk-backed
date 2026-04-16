#!/bin/bash

# 邀请返佣系统全流程测试脚本
# 用途：一键运行所有邀请相关测试，验证配置动态生效、API 端点、业务流程

set -e

echo "=========================================="
echo "邀请返佣系统全流程测试"
echo "=========================================="
echo ""

# 检查数据库连接
echo "1. 检查测试数据库连接..."
if ! docker compose exec mysql mysql -uroot -proot123456 -e "USE tokenhubhk; SELECT 1;" > /dev/null 2>&1; then
    echo "❌ 数据库连接失败，请确保 Docker Compose 服务已启动"
    exit 1
fi
echo "✅ 数据库连接正常"
echo ""

# 进入后端目录
cd backend

# 单元测试：邀请服务基础功能
echo "2. 运行单元测试：邀请服务基础功能..."
go test -v -run "^TestReferralCodeGeneration" ./internal/service/referral/
go test -v -run "^TestGetOrCreateLink" ./internal/service/referral/
go test -v -run "^TestFindByCode" ./internal/service/referral/
go test -v -run "^TestIncrementClickCount" ./internal/service/referral/
go test -v -run "^TestIncrementRegisterCount" ./internal/service/referral/
go test -v -run "^TestGetStats" ./internal/service/referral/
echo "✅ 单元测试通过"
echo ""

# 单元测试：归因解锁逻辑
echo "3. 运行单元测试：归因解锁逻辑..."
go test -v -run "^TestTryUnlockAttribution" ./internal/service/referral/
echo "✅ 归因解锁测试通过"
echo ""

# 单元测试：佣金计算器
echo "4. 运行单元测试：佣金计算器..."
go test -v -run "^TestCalculateCommissions" ./internal/service/referral/
go test -v -run "^TestLifetimeCap" ./internal/service/referral/
echo "✅ 佣金计算测试通过"
echo ""

# 集成测试：完整业务流程
echo "5. 运行集成测试：完整业务流程..."
go test -v -run "^TestReferralFlow_E2E_Complete" ./internal/service/referral/
go test -v -run "^TestReferralFlow_AttributionExpiry" ./internal/service/referral/
go test -v -run "^TestReferralFlow_UnlockThreshold" ./internal/service/referral/
go test -v -run "^TestReferralFlow_LifetimeCapReached" ./internal/service/referral/
go test -v -run "^TestReferralFlow_MultipleInvitees" ./internal/service/referral/
echo "✅ 业务流程测试通过"
echo ""

# 配置动态生效测试
echo "6. 运行配置动态生效测试..."
go test -v -run "^TestConfigDynamicUpdate_CommissionRateChange" ./internal/service/referral/
go test -v -run "^TestConfigDynamicUpdate_AttributionDaysChange" ./internal/service/referral/
go test -v -run "^TestConfigDynamicUpdate_UnlockThresholdChange" ./internal/service/referral/
go test -v -run "^TestConfigDynamicUpdate_LifetimeCapChange" ./internal/service/referral/
echo "✅ 配置动态生效测试通过"
echo ""

# API 测试：公开配置端点
echo "7. 运行 API 测试：公开配置端点..."
go test -v -run "^TestGetReferralConfig" ./internal/handler/public/
go test -v -run "^TestGetQuotaConfig" ./internal/handler/public/
go test -v -run "^TestConfigAPIs_NoDeprecatedFields" ./internal/handler/public/
echo "✅ 公开配置 API 测试通过"
echo ""

# API 测试：管理后台配置端点
echo "8. 运行 API 测试：管理后台配置端点..."
go test -v -run "^TestAdminUpdateReferralConfig" ./internal/handler/admin/
go test -v -run "^TestAdminUpdateQuotaConfig" ./internal/handler/admin/
echo "✅ 管理后台配置 API 测试通过"
echo ""

# 返回根目录
cd ..

echo "=========================================="
echo "✅ 所有测试通过！"
echo "=========================================="
echo ""
echo "测试覆盖范围："
echo "  ✓ 邀请码生成唯一性与字符集"
echo "  ✓ 邀请链接创建幂等性"
echo "  ✓ 点击/注册计数增加"
echo "  ✓ 归因创建与解锁逻辑"
echo "  ✓ 佣金计算与终身上限"
echo "  ✓ 归因窗口过期处理"
echo "  ✓ 完整邀请流程（注册→消费→解锁→佣金→结算）"
echo "  ✓ 配置动态修改后新订单生效"
echo "  ✓ 公开配置 API 响应格式"
echo "  ✓ 管理后台配置更新与验证"
echo ""
echo "下一步："
echo "  1. 前端测试：访问 /partners 页面验证动态数据展示"
echo "  2. 管理后台：修改邀请配置后验证前端同步更新"
echo "  3. 端到端测试：完整注册→邀请→消费→提现流程"
echo ""
