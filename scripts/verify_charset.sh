#!/bin/bash

# 数据库字符集配置验证脚本
# 用途：全面检查数据库字符集配置，确保符合多语言需求

set -e

echo "=========================================="
echo "数据库字符集配置验证"
echo "=========================================="
echo ""

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 检查 MySQL 容器是否运行
if ! docker compose ps mysql | grep -q "Up"; then
    echo -e "${RED}❌ MySQL 容器未运行${NC}"
    exit 1
fi

echo -e "${GREEN}✅ MySQL 容器运行中${NC}"
echo ""

# 1. 检查 MySQL 服务器配置
echo "1. 检查 MySQL 服务器配置"
echo "----------------------------------------"
docker compose exec -T mysql mysql -uroot -proot123456 -e "
SELECT
    @@character_set_server as server_charset,
    @@collation_server as server_collation,
    @@character_set_database as db_charset,
    @@collation_database as db_collation;
" 2>/dev/null

SERVER_CHARSET=$(docker compose exec -T mysql mysql -uroot -proot123456 -e "SELECT @@character_set_server;" 2>/dev/null | tail -1 | tr -d '\r')
if [ "$SERVER_CHARSET" = "utf8mb4" ]; then
    echo -e "${GREEN}✅ 服务器字符集正确: utf8mb4${NC}"
else
    echo -e "${RED}❌ 服务器字符集错误: $SERVER_CHARSET (应为 utf8mb4)${NC}"
fi
echo ""

# 2. 检查数据库字符集
echo "2. 检查数据库字符集"
echo "----------------------------------------"
docker compose exec -T mysql mysql -uroot -proot123456 -e "
SELECT DEFAULT_CHARACTER_SET_NAME, DEFAULT_COLLATION_NAME
FROM information_schema.SCHEMATA
WHERE SCHEMA_NAME = 'tokenhubhk';
" 2>/dev/null

DB_CHARSET=$(docker compose exec -T mysql mysql -uroot -proot123456 -e "
SELECT DEFAULT_CHARACTER_SET_NAME
FROM information_schema.SCHEMATA
WHERE SCHEMA_NAME = 'tokenhubhk';
" 2>/dev/null | tail -1 | tr -d '\r')

if [ "$DB_CHARSET" = "utf8mb4" ]; then
    echo -e "${GREEN}✅ 数据库字符集正确: utf8mb4${NC}"
else
    echo -e "${RED}❌ 数据库字符集错误: $DB_CHARSET (应为 utf8mb4)${NC}"
fi
echo ""

# 3. 检查表字符集
echo "3. 检查表字符集"
echo "----------------------------------------"
INCORRECT_TABLES=$(docker compose exec -T mysql mysql -uroot -proot123456 -e "
SELECT COUNT(*)
FROM information_schema.TABLES
WHERE TABLE_SCHEMA = 'tokenhubhk'
AND TABLE_COLLATION != 'utf8mb4_unicode_ci';
" 2>/dev/null | tail -1 | tr -d '\r')

TOTAL_TABLES=$(docker compose exec -T mysql mysql -uroot -proot123456 -e "
SELECT COUNT(*)
FROM information_schema.TABLES
WHERE TABLE_SCHEMA = 'tokenhubhk';
" 2>/dev/null | tail -1 | tr -d '\r')

if [ "$INCORRECT_TABLES" = "0" ]; then
    echo -e "${GREEN}✅ 所有 $TOTAL_TABLES 个表使用正确字符集${NC}"
else
    echo -e "${RED}❌ 发现 $INCORRECT_TABLES 个表字符集不正确${NC}"
    docker compose exec -T mysql mysql -uroot -proot123456 -e "
    SELECT TABLE_NAME, TABLE_COLLATION
    FROM information_schema.TABLES
    WHERE TABLE_SCHEMA = 'tokenhubhk'
    AND TABLE_COLLATION != 'utf8mb4_unicode_ci';
    " 2>/dev/null
fi
echo ""

# 4. 检查列字符集
echo "4. 检查文本列字符集"
echo "----------------------------------------"
INCORRECT_COLUMNS=$(docker compose exec -T mysql mysql -uroot -proot123456 -e "
SELECT COUNT(*)
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = 'tokenhubhk'
AND DATA_TYPE IN ('varchar', 'char', 'text', 'mediumtext', 'longtext')
AND (CHARACTER_SET_NAME != 'utf8mb4' OR COLLATION_NAME != 'utf8mb4_unicode_ci');
" 2>/dev/null | tail -1 | tr -d '\r')

if [ "$INCORRECT_COLUMNS" = "0" ]; then
    echo -e "${GREEN}✅ 所有文本列使用正确字符集${NC}"
else
    echo -e "${RED}❌ 发现 $INCORRECT_COLUMNS 个列字符集不正确${NC}"
    docker compose exec -T mysql mysql -uroot -proot123456 -e "
    SELECT TABLE_NAME, COLUMN_NAME, CHARACTER_SET_NAME, COLLATION_NAME
    FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = 'tokenhubhk'
    AND DATA_TYPE IN ('varchar', 'char', 'text', 'mediumtext', 'longtext')
    AND (CHARACTER_SET_NAME != 'utf8mb4' OR COLLATION_NAME != 'utf8mb4_unicode_ci')
    LIMIT 10;
    " 2>/dev/null
fi
echo ""

# 5. 测试中文存储
echo "5. 测试中文字符存储"
echo "----------------------------------------"
TEST_TEXT="测试中文存储 🎉"
TEST_EMAIL="charset_test_$(date +%s)@example.com"

# 清理旧的测试数据
docker compose exec -T mysql mysql -uroot -proot123456 --default-character-set=utf8mb4 -e "
USE tokenhubhk;
DELETE FROM partner_applications WHERE email LIKE 'charset_test_%@example.com';
" 2>/dev/null

# 插入测试数据
docker compose exec -T mysql mysql -uroot -proot123456 --default-character-set=utf8mb4 -e "
USE tokenhubhk;
INSERT INTO partner_applications (name, email, cooperation_type, status, created_at, updated_at)
VALUES ('$TEST_TEXT', '$TEST_EMAIL', 'other', 'pending', NOW(), NOW());
" 2>/dev/null

# 读取并验证
RETRIEVED=$(docker compose exec -T mysql mysql -uroot -proot123456 --default-character-set=utf8mb4 -e "
USE tokenhubhk;
SELECT name FROM partner_applications WHERE email = '$TEST_EMAIL';
" 2>/dev/null | tail -1 | tr -d '\r')

if [ "$RETRIEVED" = "$TEST_TEXT" ]; then
    echo -e "${GREEN}✅ 中文字符存储和读取正确${NC}"
    echo "   写入: $TEST_TEXT"
    echo "   读取: $RETRIEVED"
else
    echo -e "${RED}❌ 中文字符存储失败${NC}"
    echo "   写入: $TEST_TEXT"
    echo "   读取: $RETRIEVED"
fi

# 清理测试数据
docker compose exec -T mysql mysql -uroot -proot123456 -e "
USE tokenhubhk;
DELETE FROM partner_applications WHERE email = '$TEST_EMAIL';
" 2>/dev/null
echo ""

# 6. 检查 Go 应用连接配置
echo "6. 检查 Go 应用配置"
echo "----------------------------------------"
if grep -q "charset=utf8mb4" backend/internal/config/config.go; then
    echo -e "${GREEN}✅ DSN 包含 charset=utf8mb4${NC}"
else
    echo -e "${RED}❌ DSN 缺少 charset=utf8mb4${NC}"
fi

if grep -q "collation=utf8mb4_unicode_ci" backend/internal/config/config.go; then
    echo -e "${GREEN}✅ DSN 包含 collation=utf8mb4_unicode_ci${NC}"
else
    echo -e "${YELLOW}⚠️  DSN 缺少 collation 参数（建议添加）${NC}"
fi

if grep -q "SET NAMES utf8mb4" backend/internal/database/database.go; then
    echo -e "${GREEN}✅ GORM 回调已配置${NC}"
else
    echo -e "${RED}❌ GORM 回调未配置${NC}"
fi
echo ""

# 7. 检查 Nginx 配置
echo "7. 检查 Nginx 配置"
echo "----------------------------------------"
if grep -q "charset utf-8" docker/nginx/nginx.dev.conf; then
    echo -e "${GREEN}✅ Nginx 开发配置包含 charset utf-8${NC}"
else
    echo -e "${YELLOW}⚠️  Nginx 开发配置缺少 charset 声明${NC}"
fi

if grep -q "charset utf-8" docker/nginx/nginx.conf; then
    echo -e "${GREEN}✅ Nginx 生产配置包含 charset utf-8${NC}"
else
    echo -e "${YELLOW}⚠️  Nginx 生产配置缺少 charset 声明${NC}"
fi
echo ""

# 总结
echo "=========================================="
echo "验证完成"
echo "=========================================="
echo ""

if [ "$SERVER_CHARSET" = "utf8mb4" ] && [ "$DB_CHARSET" = "utf8mb4" ] && [ "$INCORRECT_TABLES" = "0" ] && [ "$INCORRECT_COLUMNS" = "0" ] && [ "$RETRIEVED" = "$TEST_TEXT" ]; then
    echo -e "${GREEN}✅ 所有检查通过，数据库字符集配置正确！${NC}"
    exit 0
else
    echo -e "${YELLOW}⚠️  部分检查未通过，请查看上方详细信息${NC}"
    echo ""
    echo "修复建议："
    echo "  1. 确保 docker-compose.yml 中 MySQL 配置正确"
    echo "  2. 确保 backend/internal/config/config.go DSN 包含 charset 参数"
    echo "  3. 确保 backend/internal/database/database.go 注册了 GORM 回调"
    echo "  4. 重新构建并重启服务: docker compose build && docker compose up -d"
    echo ""
    exit 1
fi
