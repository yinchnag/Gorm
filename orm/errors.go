package orm

import goredis "github.com/go-redis/redis/v8"

// goredisNil 将 goredis.Nil 导出给包内测试使用。
var goredisNil error = goredis.Nil
