package de.raindancer118.pocketcore;

import android.app.Activity;
import android.app.Dialog;
import android.content.Context;
import android.content.res.Configuration;
import android.graphics.Paint;
import android.graphics.drawable.ColorDrawable;
import android.text.SpannableStringBuilder;
import android.text.Spanned;
import android.text.style.ForegroundColorSpan;
import android.view.LayoutInflater;
import android.view.View;
import android.widget.TextView;

import androidx.appcompat.app.AppCompatDelegate;

public final class Ui {
    private Ui() {}

    /** House style links: underlined, 1px, sentence case. */
    public static void link(TextView... views) {
        for (TextView v : views) v.setPaintFlags(v.getPaintFlags() | Paint.UNDERLINE_TEXT_FLAG);
    }

    /** "$ command" with the prompt sign dimmed, the way the terminal shows it. */
    public static CharSequence prompt(Context ctx, String command) {
        return prompt(ctx, "$", command);
    }

    public static CharSequence prompt(Context ctx, String sign, String command) {
        SpannableStringBuilder sb = new SpannableStringBuilder(sign + " ");
        sb.setSpan(new ForegroundColorSpan(ctx.getColor(R.color.text3)), 0, 1, Spanned.SPAN_EXCLUSIVE_EXCLUSIVE);
        // non-breaking hyphen keeps "--since" in one piece when the line wraps
        sb.append(command.replace('-', '‑'));
        return sb;
    }

    /** Saved light/dark choice, or follow the system. */
    public static int themeMode(Context ctx) {
        return ctx.getSharedPreferences("pocketcore", Context.MODE_PRIVATE)
                .getInt("theme_mode", AppCompatDelegate.MODE_NIGHT_FOLLOW_SYSTEM);
    }

    public static boolean isDark(Context ctx) {
        return (ctx.getResources().getConfiguration().uiMode & Configuration.UI_MODE_NIGHT_MASK)
                == Configuration.UI_MODE_NIGHT_YES;
    }

    public static void toggleTheme(Activity a) {
        int mode = isDark(a) ? AppCompatDelegate.MODE_NIGHT_NO : AppCompatDelegate.MODE_NIGHT_YES;
        a.getSharedPreferences("pocketcore", Context.MODE_PRIVATE).edit().putInt("theme_mode", mode).apply();
        AppCompatDelegate.setDefaultNightMode(mode);
    }

    public interface OnConfirm {
        void run();
    }

    /** House style dialog: title, body, buttons left, primary first. */
    public static void confirm(Activity a, String title, String body, String action, boolean destructive, OnConfirm ok) {
        Dialog d = new Dialog(a);
        View v = LayoutInflater.from(a).inflate(R.layout.dialog_confirm, new android.widget.FrameLayout(a), false);
        ((TextView) v.findViewById(R.id.title)).setText(title);
        ((TextView) v.findViewById(R.id.body)).setText(body);
        TextView act = v.findViewById(R.id.action);
        act.setText(action);
        if (destructive) act.setBackgroundResource(R.drawable.bg_danger);
        act.setOnClickListener(x -> {
            d.dismiss();
            ok.run();
        });
        TextView cancel = v.findViewById(R.id.cancel);
        cancel.setOnClickListener(x -> d.dismiss());
        d.setContentView(v);
        if (d.getWindow() != null) d.getWindow().setBackgroundDrawable(new ColorDrawable(0));
        d.show();
    }
}
