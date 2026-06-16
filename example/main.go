package main

import (
	"fmt"
	"time"

	"github.com/norm/orm"
)

type ShopInfo struct {
	BuyInfo map[int64]string `orm:"name:buy_info,comment:商店购买数据"`
}

// TestUser 演示 ORM 完整用法。
// 实习生只需按此模板定义业务结构体，其余全部由框架处理。
type TestUser struct {
	orm.TableSchema[*TestUser]
	UserId   int64     `orm:"primary,name:user_id,comment:用户ID,autoInc"`
	UserName string    `orm:"name:user_name,comment:用户名,length:100,notNull"`
	Email    string    `orm:"name:email,comment:邮箱,length:255"`
	Age      int       `orm:"name:age,comment:年龄"`
	Score    float64   `orm:"name:score,comment:积分"`
	Status   int8      `orm:"name:status,comment:状态"`
	WearId   int64     `orm:"name:wear_id,comment:装备ID"`
	desc     string    `orm:"name:desc,comment:描述"`
	BuyInfo  *ShopInfo `orm:"name:buy_info,comment:商店购买数据"`
}

func main() {
	// 初始化连接池（读取 config/orm.json）
	if err := orm.InitPool("config/orm.json"); err != nil {
		panic(err)
	}
	// 优雅退出：确保所有异步 MySQL 写操作在进程退出前完成刷盘，防止数据丢失。
	defer orm.Shutdown()

	// 启动时统一执行 DDL 同步（建表/补列），与业务对象生命周期无关。
	if err := orm.MigrateTypes(&TestUser{}); err != nil {
		panic(err)
	}

	// 创建对象并初始化（仅绑定 selfPtr + 取元数据，不触发 I/O）
	user := &TestUser{
		UserId:   5001,
		UserName: "init_test",
		Age:      30,
		BuyInfo: &ShopInfo{
			BuyInfo: map[int64]string{
				1001: "Sword of Truth",
				1002: "Shield of Valor",
			},
		},
	}
	user.Init()

	// 存档：写 Redis + 异步写 MySQL
	user.Save()

	// 等待异步刷盘（sleep 时长需 > 2× FlushIntervalMs 以消除与 ticker 的竞态）
	time.Sleep(1200 * time.Millisecond)

	// 清空内存字段，从存储层加
	user.UserName = ""
	user.Age = 0
	user.BuyInfo = nil
	if err := user.Load(); err != nil {
		fmt.Printf("Load error: %v\n", err)
	} else {
		fmt.Printf("Loaded: %+v\n", user)
	}

	// 批量查询（软删除记录自动过滤）
	users, err := user.FindAll("age > 28", "age DESC", 100)
	if err != nil {
		fmt.Printf("FindAll error: %v\n", err)
	} else {
		fmt.Printf("Found %d users\n", len(users))
	}

	// 软删除
	user.Delete()

	// 等待异步刷盘
	time.Sleep(50 * time.Millisecond)
}
