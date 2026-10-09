package io.github.aerogu.lanchat

import android.content.Context
import io.github.aerogu.lanchat.mobile.Host

/** Lo que el núcleo en Go pide a Android. Go lo llama desde sus propios hilos. */
class LanChatHost(private val context: Context) : Host {
    override fun notify(title: String, body: String) = Notifications.message(context, title, body)

    override fun unreadChanged(total: Long) = Notifications.updateService(context, total.toInt())
}
