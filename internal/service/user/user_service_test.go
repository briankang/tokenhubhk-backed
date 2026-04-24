package user_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	usersvc "tokenhub-server/internal/service/user"
)

func newUserServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbName := strings.NewReplacer("/", "_", "\\", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("migrate users: %v", err)
	}
	return db
}

func seedUser(t *testing.T, db *gorm.DB, id uint, tenantID uint, email string, name string) model.User {
	t.Helper()
	u := model.User{
		BaseModel:    model.BaseModel{ID: id},
		TenantID:     tenantID,
		Email:        email,
		PasswordHash: "hash",
		Name:         name,
		IsActive:     true,
		Language:     "zh",
	}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("seed user %s: %v", email, err)
	}
	return u
}

func TestUserServiceList_SearchByEmailNameAndID(t *testing.T) {
	db := newUserServiceTestDB(t)
	svc := usersvc.NewUserService(db)
	ctx := context.Background()

	alpha := seedUser(t, db, 101, 1, "alpha-search@example.com", "Alpha Target")
	seedUser(t, db, 102, 1, "beta@example.com", "Beta Person")
	gamma := seedUser(t, db, 203, 1, "gamma@example.com", "Gamma Keyword")
	seedUser(t, db, 304, 2, "other-tenant@example.com", "Alpha Target")

	t.Run("email keyword", func(t *testing.T) {
		users, total, err := svc.List(ctx, 1, "alpha-search", 1, 20)
		if err != nil {
			t.Fatalf("list users: %v", err)
		}
		if total != 1 || len(users) != 1 || users[0].ID != alpha.ID {
			t.Fatalf("expected alpha only, total=%d users=%v", total, idsOf(users))
		}
	})

	t.Run("name keyword", func(t *testing.T) {
		users, total, err := svc.List(ctx, 1, "Keyword", 1, 20)
		if err != nil {
			t.Fatalf("list users: %v", err)
		}
		if total != 1 || len(users) != 1 || users[0].ID != gamma.ID {
			t.Fatalf("expected gamma only, total=%d users=%v", total, idsOf(users))
		}
	})

	t.Run("numeric id", func(t *testing.T) {
		users, total, err := svc.List(ctx, 1, fmt.Sprint(gamma.ID), 1, 20)
		if err != nil {
			t.Fatalf("list users: %v", err)
		}
		if total != 1 || len(users) != 1 || users[0].ID != gamma.ID {
			t.Fatalf("expected id match only, total=%d users=%v", total, idsOf(users))
		}
	})
}

func idsOf(users []model.User) []uint {
	ids := make([]uint, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.ID)
	}
	return ids
}
