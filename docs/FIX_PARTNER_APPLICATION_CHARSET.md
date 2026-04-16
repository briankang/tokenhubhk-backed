# 合作申请列表中文乱码问题修复

## 问题描述

合作申请列表（Partner Applications）中，联系人姓名等中文字段显示为乱码（`?` 或 `�`）。

## 根本原因

MySQL 数据库连接未正确设置字符集，导致：
1. 客户端连接字符集为 `latin1`（默认值）
2. Go 应用发送的 UTF-8 数据被错误解释为 `latin1`
3. 数据库存储时产生乱码（UTF-8 替换字符 `EFBFBD`）

## 修复方案

### 1. 更新 DSN 连接字符串

**文件：** `backend/internal/config/config.go`

```go
// 添加 collation 参数
return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&collation=utf8mb4_unicode_ci&parseTime=True&loc=Local",
    d.User, d.Password, d.Host, d.Port, d.DBName)
```

### 2. 添加 GORM 连接回调

**文件：** `backend/internal/database/database.go`

```go
// 注册连接初始化回调，确保每个新连接都使用 utf8mb4
DB.Callback().Create().Before("gorm:create").Register("set_charset", func(db *gorm.DB) {
    db.Exec("SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci")
})
DB.Callback().Query().Before("gorm:query").Register("set_charset", func(db *gorm.DB) {
    db.Exec("SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci")
})
DB.Callback().Update().Before("gorm:update").Register("set_charset", func(db *gorm.DB) {
    db.Exec("SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci")
})

// 立即设置当前连接的字符集
DB.Exec("SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci")
```

## 验证结果

### 修复前（记录 ID 5-7）

```sql
SELECT id, HEX(name), name FROM partner_applications WHERE id=5;
-- HEX: EFBFBDEFBFBDEFBFBDEFBFBDEFBFBDEFBFBDD6A4
-- 显示: ???????
```

### 修复后（记录 ID 8-9）

```sql
SELECT id, HEX(name), name FROM partner_applications WHERE id=9;
-- HEX: E78E8BE4BA94E6B58BE8AF95
-- 显示: 王五测试 ✅
```

## 测试验证

```bash
# 1. 重新构建并启动服务
docker compose build go-server
docker compose up -d go-server

# 2. 提交中文测试数据
python3 -c "
import urllib.request, json
data = json.dumps({
    'name':'王五测试',
    'email':'wangwu@test.com',
    'cooperation_type':'enterprise',
    'message':'测试修复后的中文存储'
}).encode('utf-8')
req = urllib.request.Request(
    'http://localhost:8090/api/v1/public/partner-applications',
    data=data,
    headers={'Content-Type':'application/json'}
)
resp = urllib.request.urlopen(req)
print(resp.read().decode('utf-8'))
"

# 3. 验证数据库存储
docker compose exec mysql mysql -uroot -proot123456 --default-character-set=utf8mb4 \
  -e "USE tokenhubhk; SELECT id, name, email FROM partner_applications ORDER BY id DESC LIMIT 1;"
```

## 影响范围

### 已修复
✅ 新提交的合作申请中文字段正常存储和显示
✅ 所有数据库操作（Create/Query/Update）都使用正确的字符集

### 历史数据
⚠️ 修复前提交的记录（ID 1-7）仍然是乱码，无法恢复
⚠️ 建议：如果这些记录重要，需要联系用户重新提交

## 部署步骤

```bash
# 1. 拉取最新代码
git pull

# 2. 重新构建 Go 服务
docker compose build go-server

# 3. 重启服务
docker compose up -d go-server

# 4. 验证修复
# 提交一条包含中文的测试申请，检查数据库存储是否正确
```

## 相关文件

- `backend/internal/config/config.go` - DSN 连接字符串配置
- `backend/internal/database/database.go` - 数据库初始化和字符集设置
- `backend/internal/handler/public/partner_application_handler.go` - 合作申请提交接口
- `backend/internal/model/partner_application.go` - 数据模型定义

## 技术细节

### 为什么需要回调？

GORM 使用连接池，每个连接可能在不同时间创建。仅在初始化时执行 `SET NAMES` 只影响当前连接，新连接仍会使用默认字符集。通过注册回调，确保每次数据库操作前都设置正确的字符集。

### 为什么 DSN 参数不够？

某些 MySQL 驱动版本对 DSN 中的 `charset` 参数支持不完整，特别是在连接池场景下。显式执行 `SET NAMES` 是最可靠的方式。

### utf8mb4 vs utf8

- `utf8mb4`：完整的 UTF-8 实现，支持 4 字节字符（如 Emoji 😀）
- `utf8`：MySQL 的伪 UTF-8，仅支持 3 字节字符（已废弃）

建议始终使用 `utf8mb4`。

---

**修复时间：** 2026-04-15  
**影响版本：** v1.0.0+  
**状态：** ✅ 已修复并验证
