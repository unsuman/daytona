// Copyright 2024 Daytona Platforms Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"fmt"
	"time"

	"github.com/glebarez/sqlite"
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var _db *gorm.DB

func GetSQLiteConnection(dbPath string) *gorm.DB {
	if _db != nil {
		return _db
	}

	log.Debug(fmt.Sprintf("Opening db at %s", dbPath))

	logLevel := log.ErrorLevel
	if log.GetLevel() == log.DebugLevel {
		logLevel = log.DebugLevel
	}

	newLogger := logger.New(
		log.New(),
		logger.Config{
			SlowThreshold:             time.Second,
			LogLevel:                  logger.LogLevel(logLevel),
			IgnoreRecordNotFoundError: true,
			ParameterizedQueries:      true,
			Colorful:                  true,
		},
	)

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: newLogger,
	})
	if err != nil {
		panic("failed to connect database")
	}
	sqlDB, err := db.DB()
	if err != nil {
		panic("failed to connect database")
	}

	sqlDB.SetMaxOpenConns(1)

	_db = db

	return _db
}
