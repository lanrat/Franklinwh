package com.github.lanrat.franklinwh.app

import android.annotation.SuppressLint
import android.app.Activity
import android.content.Intent
import android.os.Build
import android.os.Bundle
import android.provider.Settings
import android.view.WindowInsets
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.TextView
import com.github.lanrat.franklinwh.mobile.Mobile

/**
 * Shows the dashboard served by the Go library (the same page as the desktop
 * GUI) in a WebView. The server runs in-process on a random loopback port and
 * lives as long as the app process; its URL carries the access token.
 */
class MainActivity : Activity() {
    private lateinit var web: WebView

    @SuppressLint("SetJavaScriptEnabled") // the page is the app; only loopback loads in it
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        web = WebView(this)
        web.settings.javaScriptEnabled = true
        web.settings.domStorageEnabled = true
        web.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                if (request.url.host == "127.0.0.1") return false
                // Any other link opens in the browser, not inside the app.
                startActivity(Intent(Intent.ACTION_VIEW, request.url))
                return true
            }
        }
        if (Build.VERSION.SDK_INT >= 30) {
            // Android 15 draws apps edge to edge; keep the page clear of the
            // status and navigation bars and the keyboard.
            web.setOnApplyWindowInsetsListener { v, insets ->
                val bars = insets.getInsets(WindowInsets.Type.systemBars() or WindowInsets.Type.ime())
                v.setPadding(bars.left, bars.top, bars.right, bars.bottom)
                WindowInsets.CONSUMED
            }
        }
        setContentView(web)

        val deviceName = Settings.Global.getString(contentResolver, Settings.Global.DEVICE_NAME) ?: ""
        Thread {
            try {
                val url = Mobile.startServer(filesDir.absolutePath, Build.MODEL, deviceName, Build.VERSION.RELEASE)
                runOnUiThread { web.loadUrl(url) }
            } catch (e: Exception) {
                runOnUiThread {
                    setContentView(TextView(this).apply {
                        text = getString(R.string.start_failed, e.message)
                        setPadding(48, 48, 48, 48)
                    })
                }
            }
        }.start()
    }

    // Stop the page's 10-second polling while the app is in the background.
    override fun onPause() {
        web.onPause()
        web.pauseTimers()
        super.onPause()
    }

    override fun onResume() {
        super.onResume()
        web.resumeTimers()
        web.onResume()
    }

    override fun onDestroy() {
        web.destroy()
        super.onDestroy()
    }
}
