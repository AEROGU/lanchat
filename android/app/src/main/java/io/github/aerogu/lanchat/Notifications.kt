package io.github.aerogu.lanchat

import android.Manifest
import android.annotation.SuppressLint
import android.app.Notification
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import androidx.core.app.NotificationChannelCompat
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat

/** Canales y avisos de LanChat: el fijo del servicio y los de mensajes nuevos. */
object Notifications {
    const val SERVICE_ID = 1
    private const val MESSAGE_ID = 2
    private const val CHANNEL_SERVICE = "service"
    private const val CHANNEL_MESSAGES = "messages"

    fun createChannels(context: Context) {
        NotificationManagerCompat.from(context).createNotificationChannelsCompat(
            listOf(
                NotificationChannelCompat.Builder(CHANNEL_SERVICE, NotificationManagerCompat.IMPORTANCE_LOW)
                    .setName(context.getString(R.string.channel_service))
                    .setDescription(context.getString(R.string.channel_service_description))
                    .setShowBadge(false)
                    .build(),
                NotificationChannelCompat.Builder(CHANNEL_MESSAGES, NotificationManagerCompat.IMPORTANCE_HIGH)
                    .setName(context.getString(R.string.channel_messages))
                    .setDescription(context.getString(R.string.channel_messages_description))
                    .build(),
            ),
        )
    }

    /** Aviso fijo del servicio en primer plano; si hay mensajes sin leer, los cuenta. */
    fun service(context: Context, unread: Int): Notification {
        val text = if (unread > 0) {
            context.resources.getQuantityString(R.plurals.unread, unread, unread)
        } else {
            context.getString(R.string.service_running)
        }
        return NotificationCompat.Builder(context, CHANNEL_SERVICE)
            .setSmallIcon(R.drawable.ic_stat_lanchat)
            .setColor(ContextCompat.getColor(context, R.color.brand))
            .setContentTitle(context.getString(R.string.app_name))
            .setContentText(text)
            .setNumber(unread)
            .setContentIntent(openApp(context))
            .addAction(R.drawable.ic_stat_lanchat, context.getString(R.string.stop), stopService(context))
            .setOngoing(true)
            .setShowWhen(false)
            .build()
    }

    @SuppressLint("MissingPermission") // allowed() revisa POST_NOTIFICATIONS
    fun updateService(context: Context, unread: Int) {
        // Detenido, el aviso fijo ya no debe volver a aparecer.
        if (LanChatService.running && allowed(context)) {
            NotificationManagerCompat.from(context).notify(SERVICE_ID, service(context, unread))
        }
    }

    /** Aviso de mensaje nuevo; uno por conversación (el título es el remitente o la sala). */
    @SuppressLint("MissingPermission") // allowed() revisa POST_NOTIFICATIONS
    fun message(context: Context, title: String, body: String) {
        if (!allowed(context)) return
        val n = NotificationCompat.Builder(context, CHANNEL_MESSAGES)
            .setSmallIcon(R.drawable.ic_stat_lanchat)
            .setColor(ContextCompat.getColor(context, R.color.brand))
            .setContentTitle(title)
            .setContentText(body)
            .setStyle(NotificationCompat.BigTextStyle().bigText(body))
            .setCategory(NotificationCompat.CATEGORY_MESSAGE)
            .setPriority(NotificationCompat.PRIORITY_HIGH)
            .setContentIntent(openApp(context))
            .setAutoCancel(true)
            .build()
        NotificationManagerCompat.from(context).notify(title, MESSAGE_ID, n)
    }

    private fun allowed(context: Context) =
        Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU ||
            ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS) ==
            PackageManager.PERMISSION_GRANTED

    private fun stopService(context: Context): PendingIntent = PendingIntent.getService(
        context,
        0,
        Intent(context, LanChatService::class.java).setAction(LanChatService.ACTION_STOP),
        PendingIntent.FLAG_IMMUTABLE,
    )

    private fun openApp(context: Context): PendingIntent = PendingIntent.getActivity(
        context,
        0,
        Intent(context, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
        PendingIntent.FLAG_IMMUTABLE,
    )
}
