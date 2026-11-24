package de.raindancer118.pocketsudo;

import android.content.Context;
import android.util.Log;

import com.google.android.gms.tasks.Tasks;
import com.google.firebase.FirebaseApp;
import com.google.firebase.FirebaseOptions;
import com.google.firebase.messaging.FirebaseMessaging;

import org.json.JSONException;
import org.json.JSONObject;

import java.util.concurrent.TimeUnit;

/**
 * Firebase is configured at runtime from the relay's /v1/config, so the APK
 * does not depend on a particular Firebase project (no google-services.json).
 */
public final class Push {
    private static final String TAG = "PocketSudo";

    private Push() {}

    static void initFromStore(Context ctx) {
        String json = new Store(ctx).fcmOptions();
        if (json.isEmpty()) return;
        try {
            init(ctx, new JSONObject(json));
        } catch (JSONException | RuntimeException e) {
            Log.w(TAG, "firebase init", e);
        }
    }

    public static boolean ready(Context ctx) {
        return !FirebaseApp.getApps(ctx).isEmpty();
    }

    /** Initialises Firebase with the relay's options; false if the relay has no push. */
    static synchronized boolean init(Context ctx, JSONObject fcm) {
        if (fcm == null) return ready(ctx);
        String project = fcm.optString("project_id"), app = fcm.optString("app_id"),
                key = fcm.optString("api_key"), sender = fcm.optString("sender_id");
        if (project.isEmpty() || app.isEmpty() || key.isEmpty() || sender.isEmpty()) return ready(ctx);
        if (!ready(ctx)) {
            FirebaseApp.initializeApp(ctx, new FirebaseOptions.Builder()
                    .setProjectId(project).setApplicationId(app).setApiKey(key).setGcmSenderId(sender).build());
            FirebaseMessaging.getInstance().setAutoInitEnabled(true);
        }
        new Store(ctx).setFcmOptions(fcm.toString());
        return true;
    }

    /** Blocking; "" when push is unavailable. */
    static String token(Context ctx) {
        if (!ready(ctx)) return "";
        try {
            return Tasks.await(FirebaseMessaging.getInstance().getToken(), 20, TimeUnit.SECONDS);
        } catch (Exception e) {
            Log.w(TAG, "fcm token", e);
            return "";
        }
    }
}
