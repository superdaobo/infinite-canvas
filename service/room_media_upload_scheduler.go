package service

import (
	"log"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// roomMediaUploadCleanupCron 每天凌晨回收过期分片上传会话，避免中断上传长期占用磁盘。
const roomMediaUploadCleanupCron = "30 3 * * *"

var (
	roomMediaUploadCron     *cron.Cron
	roomMediaUploadCronOnce sync.Once
)

// StartRoomMediaUploadCleanupScheduler 启动过期分片上传会话清理任务，并在启动时先跑一次。
func StartRoomMediaUploadCleanupScheduler() {
	roomMediaUploadCronOnce.Do(func() {
		roomMediaUploadCron = cron.New()
		if _, err := roomMediaUploadCron.AddFunc(roomMediaUploadCleanupCron, cleanupExpiredRoomMediaUploads); err != nil {
			log.Printf("add room media upload cleanup cron failed err=%v", err)
			return
		}
		roomMediaUploadCron.Start()
	})

	cleanupExpiredRoomMediaUploads()
}

func cleanupExpiredRoomMediaUploads() {
	if removed, err := CleanupExpiredRoomMediaUploads(time.Now().UTC()); err != nil {
		log.Printf("cleanup room media uploads failed err=%v", err)
	} else if removed > 0 {
		log.Printf("cleanup room media uploads removed=%d", removed)
	}
}
