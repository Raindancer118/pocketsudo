package de.raindancer118.pocketsudo;

import de.raindancer118.pocketcore.Relay;
import de.raindancer118.pocketcore.Ui;
import de.raindancer118.pocketcore.Protocol;
import de.raindancer118.pocketcore.Push;

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

    /** Requests open on screen or decided; their notification must not come back. */
    static final java.util.Set<String> HANDLED = ConcurrentHashMap.newKeySet();

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
        Relay.userAgent = "pocketsudo-android/" + BuildConfig.VERSION_NAME;
        new Store(this).migrateToCore(this);
        AppCompatDelegate.setDefaultNightMode(Ui.themeMode(this));
        NotificationChannel ch = new NotificationChannel(CHANNEL_REQUESTS, getString(R.string.channel_requests),
                NotificationManager.IMPORTANCE_HIGH);
        ch.setDescription(getString(R.string.channel_requests_desc));
        ch.setLockscreenVisibility(android.app.Notification.VISIBILITY_PRIVATE);
        ch.enableVibration(true);
        // only honoured once the user allows it, but then sudo is not stuck behind a bedtime mode
        ch.setBypassDnd(true);
        getSystemService(NotificationManager.class).createNotificationChannel(ch);
        Push.initFromStore(this);
    }
}
