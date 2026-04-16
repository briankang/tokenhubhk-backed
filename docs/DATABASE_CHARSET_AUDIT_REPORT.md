# 数据库字符集全面检查与修复总结

## 执行时间
2026-04-15

## 问题背景

合作申请列表中联系人姓名等中文字段显示为乱码，需要全面检查数据库字符集配置，确保符合全球多语言需求。

---

## 检查范围

### ✅ 1. MySQL 服务器配置
- **配置文件：** `docker-compose.yml`
- **检查项：**
  - `character-set-server=utf8mb4` ✅
  - `collation-server=utf8mb4_unicode_ci` ✅
  - `init-connect='SET NAMES utf8mb4'` ✅ (新增)
  - `skip-character-set-client-handshake` ✅ (新增)

### ✅ 2. 数据库连接字符串（DSN）
- **文件：** `backend/internal/config/config.go`
- **检查项：**
  - `charset=utf8mb4` ✅
  - `collation=utf8mb4_unicode_ci` ✅ (新增)
  - `parseTime=True` ✅
  - `loc=Local` ✅

### ✅ 3. GORM 连接初始化
- **文件：** `backend/internal/database/database.go`
- **检查项：**
  - 注册 Create 回调 ✅ (新增)
  - 注册 Query 回调 ✅ (新增)
  - 注册 Update 回调 ✅ (新增)
  - 初始化时执行 SET NAMES ✅ (新增)

### ✅ 4. 数据库表结构
- **检查结果：**
  - 总表数：60 个
  - 使用 utf8mb4_unicode_ci：60 个 (100%)
  - 字符集不正确：0 个 ✅

### ✅ 5. 数据库列字符集
- **检查结果：**
  - 所有 varchar/text 列均使用 utf8mb4_unicode_ci ✅
  - 无字符集不一致的列 ✅

### ✅ 6. Nginx 配置
- **文件：** `docker/nginx/nginx.dev.conf` 和 `docker/nginx/nginx.conf`
- **检查项：**
  - 开发环境添加 `charset utf-8` ✅ (新增)
  - 生产环境添加 `charset utf-8` ✅ (新增)

### ✅ 7. 测试文件 DSN
- **检查范围：** `backend/internal/service/*/test*.go`
- **检查结果：**
  - 所有测试文件 DSN 均包含 `charset=utf8mb4` ✅

---

## 修复内容

### 1. MySQL 服务器配置增强

**文件：** `docker-compose.yml`

```yaml
command: >
  --default-authentication-plugin=mysql_native_password
  --character-set-server=utf8mb4
  --collation-server=utf8mb4_unicode_ci
  --init-connect='SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci'  # 新增
  --skip-character-set-client-handshake                          # 新增
  --innodb-buffer-pool-size=256M
  --max-connections=200
  --innodb-log-file-size=64M
```

**说明：**
- `--init-connect`：每个新连接自动设置字符集
- `--skip-character-set-client-handshake`：忽略客户端字符集协商，强制使用服务器配置

### 2. DSN 添加 collation 参数

**文件：** `backend/internal/config/config.go`

```go
func (d *DatabaseConfig) DSN() string {
    return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&collation=utf8mb4_unicode_ci&parseTime=True&loc=Local",
        d.User, d.Password, d.Host, d.Port, d.DBName)
}
```

### 3. GORM 连接回调

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

### 4. Nginx 字符集声明

**文件：** `docker/nginx/nginx.dev.conf` 和 `docker/nginx/nginx.conf`

```nginx
server {
    listen 80;
    server_name localhost;
    
    # 确保正确处理 UTF-8 字符
    charset utf-8;  # 新增
    
    # ... 其他配置
}
```

---

## 新增文件

### 1. UTF-8 测试套件

**文件：** `backend/internal/database/utf8_test.go`

**测试覆盖：**
- ✅ 中文（简体/繁体）
- ✅ 日文、韩文
- ✅ 阿拉伯文、俄文
- ✅ 德文、法文、西班牙文
- ✅ 泰文、越南文
- ✅ Emoji 表情符号
- ✅ 混合语言
- ✅ 特殊符号

**测试用例：**
- `TestUTF8Support`：测试 14 种语言/字符集的存储和读取
- `TestDatabaseCharsetConfiguration`：验证数据库字符集配置
- `TestTableCharsetConfiguration`：验证所有表的字符集配置

### 2. 字符集验证脚本

**文件：** `backend/scripts/verify_charset.sh`

**功能：**
- 检查 MySQL 服务器配置
- 检查数据库字符集
- 检查表字符集
- 检查列字符集
- 测试中文存储
- 检查 Go 应用配置
- 检查 Nginx 配置

**使用方法：**
```bash
bash backend/scripts/verify_charset.sh
```

### 3. 最佳实践文档

**文件：** `backend/docs/DATABASE_CHARSET_BEST_PRACTICES.md`

**内容：**
- 配置清单（MySQL/DSN/GORM/Nginx）
- 验证方法
- 常见问题排查
- 修复历史数据的方法
- 开发规范

### 4. 乱码修复文档

**文件：** `backend/docs/FIX_PARTNER_APPLICATION_CHARSET.md`

**内容：**
- 问题描述
- 根本原因
- 修复方案
- 验证结果
- 部署步骤

---

## 验证结果

### 自动化验证

```bash
bash backend/scripts/verify_charset.sh
```

**输出：**
```
✅ MySQL 容器运行中
✅ 服务器字符集正确: utf8mb4
✅ 数据库字符集正确: utf8mb4
✅ 所有 60 个表使用正确字符集
✅ 所有文本列使用正确字符集
✅ 中文字符存储和读取正确
   写入: 测试中文存储 🎉
   读取: 测试中文存储 🎉
✅ DSN 包含 charset=utf8mb4
✅ DSN 包含 collation=utf8mb4_unicode_ci
✅ GORM 回调已配置
✅ Nginx 开发配置包含 charset utf-8
✅ Nginx 生产配置包含 charset utf-8

✅ 所有检查通过，数据库字符集配置正确！
```

### 手动验证

**测试中文存储：**
```bash
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
```

**数据库验证：**
```bash
docker compose exec mysql mysql -uroot -proot123456 --default-character-set=utf8mb4 -e "
USE tokenhubhk;
SELECT id, name, email FROM partner_applications ORDER BY id DESC LIMIT 1;
"
```

**结果：**
```
id    name        email
9     王五测试    wangwu@test.com  ✅
```

---

## 影响范围

### 已修复
✅ 新提交的数据使用正确字符集存储  
✅ 所有数据库操作（Create/Query/Update）都使用 utf8mb4  
✅ HTTP 响应正确声明 UTF-8 字符集  
✅ 支持全球所有语言和 Emoji  

### 历史数据
⚠️ 修复前提交的记录（ID 1-7）仍然是乱码，无法自动恢复  
⚠️ 建议：如果这些记录重要，需要联系用户重新提交  

---

## 部署清单

### 1. 代码变更
- [x] `docker-compose.yml` - MySQL 配置增强
- [x] `backend/internal/config/config.go` - DSN 添加 collation
- [x] `backend/internal/database/database.go` - GORM 回调
- [x] `docker/nginx/nginx.dev.conf` - 添加 charset
- [x] `docker/nginx/nginx.conf` - 添加 charset

### 2. 新增文件
- [x] `backend/internal/database/utf8_test.go` - UTF-8 测试套件
- [x] `backend/scripts/verify_charset.sh` - 验证脚本
- [x] `backend/docs/DATABASE_CHARSET_BEST_PRACTICES.md` - 最佳实践
- [x] `backend/docs/FIX_PARTNER_APPLICATION_CHARSET.md` - 修复文档

### 3. 部署步骤
```bash
# 1. 拉取最新代码
git pull

# 2. 重启 MySQL（应用新配置）
docker compose restart mysql

# 3. 重新构建 Go 服务
docker compose build go-server

# 4. 重启所有服务
docker compose up -d

# 5. 验证配置
bash backend/scripts/verify_charset.sh

# 6. 运行测试
cd backend
go test -v ./internal/database/ -run TestUTF8Support
```

---

## 开发规范

### 1. 新建表时
```go
type NewModel struct {
    Name string `gorm:"type:varchar(100);not null" json:"name"`
}
```
- GORM AutoMigrate 会自动使用 utf8mb4_unicode_ci
- 无需额外配置

### 2. 手动建表时
```sql
CREATE TABLE new_table (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    name VARCHAR(100) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
```

### 3. 测试文件 DSN
```go
dsn := "root:password@tcp(localhost:3306)/dbname?charset=utf8mb4&collation=utf8mb4_unicode_ci&parseTime=True&loc=Local"
```

### 4. 验证新功能
- 提交包含中文/Emoji 的测试数据
- 查询数据库验证存储正确
- 运行 `verify_charset.sh` 脚本

---

## 监控建议

### 1. 定期验证
```bash
# 每周运行一次验证脚本
bash backend/scripts/verify_charset.sh
```

### 2. 新表检查
```bash
# 检查新建表的字符集
docker compose exec mysql mysql -uroot -proot123456 -e "
SELECT TABLE_NAME, TABLE_COLLATION
FROM information_schema.TABLES
WHERE TABLE_SCHEMA = 'tokenhubhk'
AND TABLE_COLLATION != 'utf8mb4_unicode_ci';
"
```

### 3. 测试覆盖
```bash
# 运行 UTF-8 测试套件
cd backend
go test -v ./internal/database/ -run TestUTF8Support
```

---

## 总结

### 完成情况
✅ **MySQL 服务器配置** - 强制所有连接使用 utf8mb4  
✅ **DSN 连接字符串** - 显式声明 charset 和 collation  
✅ **GORM 连接回调** - 确保连接池中所有连接正确配置  
✅ **Nginx 配置** - HTTP 响应头声明 UTF-8  
✅ **测试套件** - 覆盖 14 种语言和特殊字符  
✅ **验证脚本** - 自动化检查所有配置项  
✅ **文档完善** - 最佳实践和故障排查指南  

### 质量保证
✅ 所有 60 个表使用正确字符集  
✅ 所有文本列使用正确字符集  
✅ 中文、Emoji 等多语言字符存储正确  
✅ 自动化测试覆盖全面  
✅ 验证脚本一键检查  

### 后续维护
- 定期运行验证脚本
- 新建表时遵循开发规范
- 测试时包含多语言用例
- 监控生产环境数据质量

---

**检查完成时间：** 2026-04-15  
**检查人员：** Claude (Kiro)  
**状态：** ✅ 全部通过
