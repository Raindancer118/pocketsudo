package de.raindancer118.pocketsudo;

import de.raindancer118.pocketcore.Protocol;

import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;
import android.util.Log;

/** "Deny" straight from the notification, no fingerprint needed. */
public class DenyReceiver extends BroadcastReceiver {
    @Override
    public void onReceive(Context ctx, Intent intent) {
        String channel = intent.getStringExtra(ApprovalActivity.EXTRA_CHANNEL);
        String id = intent.getStringExtra(ApprovalActivity.EXTRA_ID);
        if (channel == null || id == null) return;
        Requests.cancelNotification(ctx, id);
        PendingResult pr = goAsync();
        App.IO.execute(() -> {
            try {
                Store store = new Store(ctx);
                Store.Pairing p = store.pairing(channel);
                if (p != null) {
                    Requests.deny(p, id);
                    Protocol.Request r = App.SEEN.remove(id);
                    if (r != null) store.record(r, Store.OUT_DENIED);
                }
            } catch (Exception e) {
                Log.w("PocketSudo", "deny", e);
            } finally {
                pr.finish();
            }
        });
    }
}
