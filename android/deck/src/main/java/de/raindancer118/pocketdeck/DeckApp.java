package de.raindancer118.pocketdeck;

import android.app.Application;

import androidx.appcompat.app.AppCompatDelegate;

import de.raindancer118.pocketcore.Push;
import de.raindancer118.pocketcore.Relay;
import de.raindancer118.pocketcore.Ui;

public class DeckApp extends Application {
    @Override
    public void onCreate() {
        super.onCreate();
        Relay.userAgent = "pocketdeck-android/" + BuildConfig.VERSION_NAME;
        AppCompatDelegate.setDefaultNightMode(Ui.themeMode(this));
        Push.initFromStore(this);
        Incoming.createChannel(this);
    }
}
