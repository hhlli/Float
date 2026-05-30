package logger

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

var Log *zap.Logger

func Init() {
	// 配置 JSON 格式
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	// 配置文件轮转
	fileSyncer := zapcore.AddSync(&lumberjack.Logger{
		Filename:   "logs/server.log", // 挂载至 Docker volumes
		MaxSize:    10,                // 每个日志文件最大 10 MB
		MaxBackups: 5,                 // 保留 5 个旧文件
		MaxAge:     30,                // 保留 30 天
		Compress:   true,              // 压缩旧文件
	})

	// 混合输出：控制台 + 文件
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		zapcore.NewMultiWriteSyncer(zapcore.AddSync(os.Stdout), fileSyncer),
		zap.InfoLevel,
	)

	Log = zap.New(core, zap.AddCaller())

	// 接管标准库 log 输出，防止旧代码格式撕裂
	zap.RedirectStdLog(Log)
}