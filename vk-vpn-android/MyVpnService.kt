package com.vkvpn.client

import android.content.Intent
import android.net.VpnService
import android.os.ParcelFileDescriptor
import android.util.Log
import mobile.Mobile
import mobile.ProtectSocketCallback

class MyVpnService : VpnService() {

    private var tunFd: ParcelFileDescriptor? = null
    private var isRunning = false

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val uri = intent?.getStringExtra("URI") ?: return START_NOT_STICKY
        val vp8Fps = intent.getIntExtra("FPS", 24)
        val vp8Batch = intent.getIntExtra("BATCH", 30)

        Thread {
            startVpnInternal(uri, vp8Fps, vp8Batch)
        }.start()

        return START_STICKY
    }

    private fun startVpnInternal(uri: String, fps: Int, batch: Int) {
        if (isRunning) return

        try {
            // 1. Configure the TUN interface
            val builder = Builder()
                .setSession("VK VPN")
                .addAddress("10.8.0.2", 24)
                .addRoute("0.0.0.0", 0) // Route all traffic
                .addDnsServer("8.8.8.8")
                .setMtu(1280)
                // Optionally allow specific apps to bypass if needed

            tunFd = builder.establish()
            if (tunFd == null) {
                Log.e("VKVPN", "Failed to establish TUN interface (unauthorized or conflicting VPN)")
                return
            }

            val fdInt = tunFd!!.fd
            Log.i("VKVPN", "TUN interface established. FD: $fdInt")

            // 2. Setup Routing Loop Protection via Callbacks
            // We implement the Go interface here so Go can ask Android to protect specific sockets.
            val protector = object : ProtectSocketCallback {
                override fun protectSocket(fd: Int): Boolean {
                    Log.d("VKVPN", "Protecting socket FD: $fd")
                    return this@MyVpnService.protect(fd)
                }
            }
            Mobile.setProtector(protector)

            // 3. Start Go Core
            // We pass the raw File Descriptor to Go. Go will wrap it in os.NewFile
            // and use gVisor to read/write raw IP packets to it.
            Mobile.startVPN(uri, fdInt, fps, batch)

            isRunning = true

        } catch (e: Exception) {
            Log.e("VKVPN", "Error starting VPN", e)
            stopVpnInternal()
        }
    }

    override fun onDestroy() {
        stopVpnInternal()
        super.onDestroy()
    }

    private fun stopVpnInternal() {
        if (!isRunning) return
        isRunning = false
        
        try {
            Mobile.stopVPN() // Stop Go routines
        } catch (e: Exception) {
            Log.e("VKVPN", "Error stopping Go VPN", e)
        }

        try {
            tunFd?.close()
            tunFd = null
        } catch (e: Exception) {
            Log.e("VKVPN", "Error closing TUN interface", e)
        }

        Log.i("VKVPN", "VPN Service stopped.")
    }
}
