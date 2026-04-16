# 数据库字符集配置最佳实践

## 概述

本文档说明 TokenHub 平台如何确保全球多语言支持，防止字符乱码问题。

## 核心原则

✅ **全栈 UTF-8**：从数据库到应用层到前端，统一使用 UTF-8 编码  
✅ **utf8mb4**：使用 MySQL 的 `utf8mb4` 字符集（完整 UTF-8，支持 Emoji）  
✅ **显式配置**：在每个层级显式声明字符集，不依赖默认值  
✅ **测试覆盖**：包含多语言测试用例，验证各种字符正确存储

---

## 配置清单

### 1. MySQL 服务器配置

**文件：** `docker-compose.yml`

```yaml
mysql:
  command: >
    --default-authentication-plugin=mysql_native_password
    --character-set-server=utf8mb4
    --collation-server=utf8mb4_unicode_ci
    --init-connect='SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci'
    --skip-character-set-client-handshake
```

**说明：**
- `--character-set-server=utf8mb4`：服务器默认字符集
- `--collation-server=utf8mb4_unicode_ci`：服务器默认排序规则
- `--init-connect='SET NAMES utf8mb4'`：每个新连接自动设置字符集
- `--skip-character-set-client-handshake`：忽略客户端字符集协商，强制使用服务器配置

### 2. 数据库连接字符串（DSN）

**文件：** `backend/internal/config/config.go`

```go
func (d *DatabaseConfig) DSN() string {
    return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&collation=utf8mb4_unicode_ci&parseTime=True&loc=Local",
        d.User, d.Password, d.Host, d.Port, d.DBName)
}
```

**关键参数：**
- `charset=utf8mb4`：连接字符集
- `collation=utf8mb4_unicode_ci`：排序规则（支持多语言排序）
- `parseTime=True`：正确解析 MySQL 时间类型
- `loc=Local`：使用本地时区

### 3. GORM 连接初始化

**文件：** `backend/internal/database/database.go`

```go
DB, err = gorm.Open(mysql.Open(dsn), &gorm.Config{
    Logger: gormlogger.Default.LogMode(logLevel),
    DisableForeignKeyConstraintWhenMigrating: true,
})

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

**为什么需要回调？**

GORM 使用连接池，每个连接可能在不同时间创建。仅在初始化时执行 `SET NAMES` 只影响当前连接，新连接仍会使用默认字符集。通过注册回调，确保每次数据库操作前都设置正确的字符集。

### 4. 表结构定义

**GORM 模型标签：**

```go
type User struct {
    Name  string `gorm:"type:varchar(100);not null" json:"name"`
    Email string `gorm:"type:varchar(200);not null" json:"email"`
}
```

**自动迁移：**

GORM AutoMigrate 会自动使用数据库默认字符集（`utf8mb4_unicode_ci`），无需额外配置。

**手动建表（如需要）：**

```sql
CREATE TABLE users (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    email VARCHAR(200) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
```

### 5. Nginx 配置

**文件：** `docker/nginx/nginx.dev.conf` 和 `docker/nginx/nginx.conf`

```nginx
server {
    listen 80;
    server_name localhost;
    
    # 确保正确处理 UTF-8 字符
    charset utf-8;
    
    # ... 其他配置
}
```

**说明：**
- `charset utf-8`：设置 HTTP 响应头 `Content-Type: text/html; charset=utf-8`
- 确保浏览器正确解析 HTML 中的多语言字符

### 6. Go HTTP 响应

**文件：** `backend/internal/pkg/response/response.go`

```go
func Success(c *gin.Context, data interface{}) {
    c.JSON(http.StatusOK, gin.H{
        "code":    0,
        "message": "ok",
        "data":    data,
    })
}
```

**Gin 框架默认行为：**
- 自动设置 `Content-Type: application/json; charset=utf-8`
- JSON 序列化时保留 UTF-8 字符（不转义为 `\uXXXX`）

---

## 验证方法

### 1. 检查 MySQL 服务器配置

```bash
docker compose exec mysql mysql -uroot -proot123456 -e "
SHOW VARIABLES LIKE 'character_set%';
SHOW VARIABLES LIKE 'collation%';
"
```

**预期输出：**
```
character_set_server    utf8mb4
collation_server        utf8mb4_unicode_ci
```

### 2. 检查数据库字符集

```bash
docker compose exec mysql mysql -uroot -proot123456 -e "
SELECT DEFAULT_CHARACTER_SET_NAME, DEFAULT_COLLATION_NAME 
FROM information_schema.SCHEMATA 
WHERE SCHEMA_NAME = 'tokenhubhk';
"
```

**预期输出：**
```
utf8mb4    utf8mb4_unicode_ci
```

### 3. 检查表字符集

```bash
docker compose exec mysql mysql -uroot -proot123456 -e "
SELECT TABLE_NAME, TABLE_COLLATION 
FROM information_schema.TABLES 
WHERE TABLE_SCHEMA = 'tokenhubhk' 
LIMIT 5;
"
```

**预期输出：**
```
users                   utf8mb4_unicode_ci
partner_applications    utf8mb4_unicode_ci
...
```

### 4. 检查列字符集

```bash
docker compose exec mysql mysql -uroot -proot123456 -e "
SELECT TABLE_NAME, COLUMN_NAME, CHARACTER_SET_NAME, COLLATION_NAME 
FROM information_schema.COLUMNS 
WHERE TABLE_SCHEMA = 'tokenhubhk' 
AND DATA_TYPE IN ('varchar', 'text') 
AND (CHARACTER_SET_NAME != 'utf8mb4' OR COLLATION_NAME != 'utf8mb4_unicode_ci')
LIMIT 10;
"
```

**预期输出：**
```
Empty set (0.00 sec)
```

如果有输出，说明存在字符集不一致的列，需要修复。

### 5. 运行自动化测试

```bash
cd backend
go test -v ./internal/database/ -run TestUTF8Support
go test -v ./internal/database/ -run TestDatabaseCharsetConfiguration
go test -v ./internal/database/ -run TestTableCharsetConfiguration
```

**测试覆盖：**
- ✅ 中文（简体/繁体）
- ✅ 日文
- ✅ 韩文
- ✅ 阿拉伯文
- ✅ 俄文
- ✅ 德文、法文、西班牙文
- ✅ 泰文、越南文
- ✅ Emoji 表情符号
- ✅ 混合语言
- ✅ 特殊符号

### 6. 手动测试

```bash
# 提交包含中文的合作申请
curl -X POST http://localhost/api/v1/public/partner-applications \
  -H "Content-Type: application/json" \
  --data-binary '{"name":"张三测试","email":"test@example.com","cooperation_type":"enterprise","message":"测试中文存储"}'

# 查询数据库验证
docker compose exec mysql mysql -uroot -proot123456 --default-character-set=utf8mb4 -e "
USE tokenhubhk;
SELECT id, name, email FROM partner_applications ORDER BY id DESC LIMIT 1;
"
```

**预期输出：**
```
id    name        email
123   张三测试    test@example.com
```

---

## 常见问题排查

### 问题1：数据库中显示乱码（`?` 或 `�`）

**原因：** 连接字符集不正确

**排查步骤：**

1. 检查连接字符集：
```sql
SELECT @@character_set_client, @@character_set_connection, @@character_set_results;
```

2. 如果不是 `utf8mb4`，检查：
   - DSN 是否包含 `charset=utf8mb4`
   - GORM 回调是否正确注册
   - MySQL 服务器是否配置 `--init-connect`

**修复：**
- 确保 DSN 包含 `charset=utf8mb4&collation=utf8mb4_unicode_ci`
- 确保 GORM 回调已注册（见上文）
- 重启 Go 服务

### 问题2：已存在的乱码数据无法恢复

**原因：** 数据已损坏，UTF-8 字节被错误解释为 latin1 并存储

**解决方案：**
- 无法自动恢复
- 需要用户重新提交数据
- 或者从备份中恢复（如果备份时字符集正确）

### 问题3：Emoji 显示为 `?`

**原因：** 使用了 `utf8` 而不是 `utf8mb4`

**修复：**
```sql
-- 修改表字符集
ALTER TABLE table_name CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- 修改列字符集
ALTER TABLE table_name MODIFY column_name VARCHAR(100) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
```

### 问题4：排序不正确（中文/日文等）

**原因：** 使用了 `utf8mb4_general_ci` 而不是 `utf8mb4_unicode_ci`

**区别：**
- `utf8mb4_general_ci`：快速但不准确的排序（不支持多语言）
- `utf8mb4_unicode_ci`：准确的 Unicode 排序（支持多语言）

**修复：**
```sql
ALTER TABLE table_name CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
```

---

## 性能考虑

### utf8mb4 vs utf8

- **存储空间：** utf8mb4 每个字符最多 4 字节，utf8 最多 3 字节
- **性能影响：** 可忽略（现代硬件下差异 < 1%）
- **建议：** 始终使用 utf8mb4，避免未来兼容性问题

### utf8mb4_unicode_ci vs utf8mb4_general_ci

- **排序准确性：** unicode_ci 更准确，支持多语言
- **性能：** general_ci 稍快（约 5-10%），但排序不准确
- **建议：** 使用 unicode_ci，除非有极端性能需求

### 索引长度限制

MySQL InnoDB 索引最大长度：767 字节（MySQL 5.7）或 3072 字节（MySQL 8.0）

**utf8mb4 下：**
- VARCHAR(191) = 191 × 4 = 764 字节（MySQL 5.7 安全）
- VARCHAR(768) = 768 × 4 = 3072 字节（MySQL 8.0 安全）

**建议：**
- 邮箱、用户名等索引列：VARCHAR(200) 以内
- 如需更长索引，使用前缀索引：`INDEX idx_name (name(100))`

---

## 迁移指南

### 从 utf8 迁移到 utf8mb4

```sql
-- 1. 备份数据库
mysqldump -uroot -p tokenhubhk > backup.sql

-- 2. 修改数据库默认字符集
ALTER DATABASE tokenhubhk CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- 3. 修改所有表
SELECT CONCAT('ALTER TABLE ', TABLE_NAME, ' CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;')
FROM information_schema.TABLES
WHERE TABLE_SCHEMA = 'tokenhubhk';

-- 4. 执行生成的 ALTER TABLE 语句
-- （复制上一步输出，逐条执行）

-- 5. 验证
SELECT TABLE_NAME, TABLE_COLLATION 
FROM information_schema.TABLES 
WHERE TABLE_SCHEMA = 'tokenhubhk' 
AND TABLE_COLLATION != 'utf8mb4_unicode_ci';
```

### 修复已损坏的数据

**如果数据是 UTF-8 但被错误存储为 latin1：**

```sql
-- 1. 临时转换为 binary
ALTER TABLE table_name MODIFY column_name VARBINARY(300);

-- 2. 转换为 utf8mb4
ALTER TABLE table_name MODIFY column_name VARCHAR(100) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
```

**注意：** 此方法仅在数据实际是 UTF-8 字节但被错误标记为 latin1 时有效。如果数据已损坏（如 `EFBFBD`），无法恢复。

---

## 测试清单

### 开发阶段

- [ ] 所有表使用 `utf8mb4_unicode_ci`
- [ ] DSN 包含 `charset=utf8mb4&collation=utf8mb4_unicode_ci`
- [ ] GORM 回调已注册
- [ ] Nginx 配置 `charset utf-8`
- [ ] 运行 `TestUTF8Support` 测试通过

### 部署前

- [ ] 检查 MySQL 服务器配置
- [ ] 检查数据库字符集
- [ ] 检查所有表字符集
- [ ] 手动测试中文/Emoji 提交
- [ ] 验证 API 响应 Content-Type

### 生产环境

- [ ] 监控字符集相关错误日志
- [ ] 定期运行字符集验证脚本
- [ ] 备份策略包含字符集信息

---

## 参考资料

- [MySQL 8.0 Character Sets and Collations](https://dev.mysql.com/doc/refman/8.0/en/charset.html)
- [GORM MySQL Driver](https://gorm.io/docs/connecting_to_the_database.html#MySQL)
- [Unicode Standard](https://unicode.org/standard/standard.html)
- [UTF-8 Everywhere Manifesto](http://utf8everywhere.org/)

---

**文档版本：** v1.0  
**最后更新：** 2026-04-15  
**维护者：** TokenHub 开发团队
