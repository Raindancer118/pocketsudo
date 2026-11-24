package de.raindancer118.pocketsudo;

import android.os.Bundle;
import android.text.format.DateUtils;
import android.view.LayoutInflater;
import android.view.View;
import android.widget.LinearLayout;
import android.widget.TextView;

import androidx.appcompat.app.AppCompatActivity;

import java.util.List;

public class HistoryActivity extends AppCompatActivity {
    @Override
    protected void onCreate(Bundle s) {
        super.onCreate(s);
        setContentView(R.layout.activity_history);
        findViewById(R.id.back).setOnClickListener(v -> finish());
        List<Store.Entry> entries = new Store(this).history();
        findViewById(R.id.empty).setVisibility(entries.isEmpty() ? View.VISIBLE : View.GONE);
        findViewById(R.id.scroll).setVisibility(entries.isEmpty() ? View.GONE : View.VISIBLE);
        LinearLayout list = findViewById(R.id.list);
        LayoutInflater inf = LayoutInflater.from(this);
        for (Store.Entry e : entries) {
            View row = inf.inflate(R.layout.item_history, list, false);
            ((TextView) row.findViewById(R.id.command)).setText(Ui.prompt(this, e.command));
            String when = DateUtils.formatDateTime(this, e.time,
                    DateUtils.isToday(e.time) ? DateUtils.FORMAT_SHOW_TIME
                            : DateUtils.FORMAT_SHOW_DATE | DateUtils.FORMAT_SHOW_TIME | DateUtils.FORMAT_ABBREV_MONTH);
            ((TextView) row.findViewById(R.id.meta)).setText(getString(R.string.history_meta, e.hostname, outcome(e.outcome), when));
            list.addView(row);
        }
    }

    private String outcome(String o) {
        switch (o) {
            case Store.OUT_APPROVED:
                return getString(R.string.approved_finger);
            case Store.OUT_DENIED:
                return getString(R.string.denied);
            case Store.OUT_EXPIRED:
                return getString(R.string.expired);
            case Store.OUT_ELSEWHERE:
                return getString(R.string.decided_elsewhere);
            default:
                return getString(R.string.bad_signature);
        }
    }
}
