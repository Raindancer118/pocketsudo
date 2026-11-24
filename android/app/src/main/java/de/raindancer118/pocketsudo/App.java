package de.raindancer118.pocketsudo;

import android.app.Application;
import android.app.NotificationChannel;
import android.app.NotificationManager;

import androidx.appcompat.app.AppCompatDelegate;

import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

public class App extends Application {
    public static final String CHANNEL_REQUESTS = "requests";
    public static final ExecutorService IO = Executors.newCachedThreadPool();

    /** Requests seen by this process, so a cancel push can be written to the history. */
    static final Map<String, Protocol.Request> SEEN = new ConcurrentHashMap<>();

    public interface CancelListener {
        void onCancelled(String requestId);
    }

    private static final CopyOnWriteArrayList<CancelListener> LISTENERS = new CopyOnWriteArrayList<>();

    public static void addCancelListener(CancelListener l) {
        LISTENERS.add(l);
    }

    public static void removeCancelListener(CancelListener l) {
        LISTENERS.remove(l);
    }

    static void fireCancelled(String id) {
        for (CancelListener l : LISTENERS) l.onCancelled(id);
    }

    @Override
    public void onCreate() {
        super.onCreate();
        AppCompatDelegate.setDefaultNightMode(new Store(this).themeMode(AppCompatDelegate.MODE_NIGHT_FOLLOW_SYSTEM));
        NotificationChannel ch = new NotificationChannel(CHANNEL_REQUESTS, getString(R.string.channel_requests),
                NotificationManager.IMPORTANCE_HIGH);
        ch.setDescription(getString(R.string.channel_requests_desc));
        ch.setLockscreenVisibility(android.app.Notification.VISIBILITY_PRIVATE);
        ch.enableVibration(true);
        getSystemService(NotificationManager.class).createNotificationChannel(ch);
        Push.initFromStore(this);
    }
}
