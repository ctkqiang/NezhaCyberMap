/*
NezhaApp.kt —— Application 入口 + 全局崩溃取证。

为什么要有 Application 而不是只在 MainActivity 里注册：
  启动崩溃未必发生在 onCreate 里。资源/主题解析、ContentProvider 初始化、
  类加载失败都在 Application 阶段就炸了，那时 MainActivity 的代码一行都还没跑。
  把 handler 挂在这里，才覆盖得住"启动即崩"的完整时间窗。

为什么同时写两处：
  files/crash.txt           —— adb run-as 可取，不需要 root；
  getExternalFilesDir 下同名文件 —— 用桌面文件管理器 / Android Studio 的
                                    Device Explorer 直接能翻到，省掉一条命令。
  两处都失败也不影响进程正常死亡：取证是尽力而为，不能反噬主流程。
*/
package com.nezha.cybermap

import android.app.Application
import android.os.Build

class NezhaApp : Application() {
    override fun onCreate() {
        installCrashGuard(this)
        super.onCreate()
    }
}

private const val CRASH_FILE = "crash.txt"
private const val START_FILE = "last-start.txt"

/**
 * 安装全局未捕获异常记录器。
 *
 * 记录内容刻意包含 Build 信息与前台 Activity 名：
 * 真机崩溃报告里最常见的缺口就是"不知道哪台机器、哪个系统版本"，
 * 有了这两行，复现条件就不必再靠猜。
 */
fun installCrashGuard(app: Application) {
    val previous = Thread.getDefaultUncaughtExceptionHandler()

    runCatching {
        app.writeDiagnostic(
            START_FILE,
            buildString {
                appendLine("sdk=${Build.VERSION.SDK_INT}")
                appendLine("device=${Build.MANUFACTURER} ${Build.MODEL}")
                appendLine("abi=${Build.SUPPORTED_ABIS.joinToString()}")
                appendLine("startedAt=${System.currentTimeMillis()}")
            },
        )
    }

    Thread.setDefaultUncaughtExceptionHandler { thread, error ->
        runCatching {
            app.writeDiagnostic(
                CRASH_FILE,
                buildString {
                    appendLine("thread=${thread.name}")
                    appendLine("time=${System.currentTimeMillis()}")
                    appendLine("sdk=${Build.VERSION.SDK_INT}")
                    appendLine("device=${Build.MANUFACTURER} ${Build.MODEL}")
                    appendLine("version=${Build.VERSION.RELEASE}")
                    appendLine("---")
                    appendLine(error.stackTraceToString())
                },
            )
        }
        previous?.uncaughtException(thread, error)
    }
}

/** 内部存储与外部私有目录各写一份；任一成功即可。 */
private fun Application.writeDiagnostic(name: String, text: String) {
    runCatching { java.io.File(filesDir, name).writeText(text) }
    getExternalFilesDir(null)?.let { dir ->
        runCatching { java.io.File(dir, name).writeText(text) }
    }
}
