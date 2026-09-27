package io.github.jeremiahm37.lectern

import android.app.AlertDialog
import android.content.Intent
import android.graphics.Typeface
import android.os.Bundle
import android.util.TypedValue
import android.view.Gravity
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import androidx.activity.ComponentActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat

/**
 * The Lecterns this app is paired with: tap one to open it, hold one to
 * rename or remove it, or add another. Reached from Settings → Devices in
 * the app, and from the launcher icon's long-press menu.
 */
class HostsActivity : ComponentActivity() {
    private lateinit var hosts: Hosts
    private lateinit var list: LinearLayout

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        hosts = Hosts(this)
        val pad = dp(20)
        val column = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(pad, pad, pad, pad)
        }
        column.addView(TextView(this).apply {
            text = getString(R.string.hosts_title)
            setTextColor(getColor(R.color.lectern_fg))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 24f)
            typeface = Typeface.DEFAULT_BOLD
            setPadding(0, 0, 0, dp(12))
        })
        list = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        column.addView(list)
        column.addView(Button(this).apply {
            id = R.id.add_host
            text = getString(R.string.add_host)
            setOnClickListener { startActivity(Intent(this@HostsActivity, ConnectActivity::class.java)) }
        }, LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT).apply { topMargin = dp(16) })
        val scroll = ScrollView(this).apply { addView(column) }
        ViewCompat.setOnApplyWindowInsetsListener(scroll) { v, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            v.setPadding(bars.left, bars.top, bars.right, bars.bottom)
            WindowInsetsCompat.CONSUMED
        }
        setContentView(scroll)
    }

    override fun onResume() {
        super.onResume()
        render()
    }

    private fun render() {
        hosts.pruneUnpaired()
        list.removeAllViews()
        val active = hosts.activeId
        val paired = hosts.all().filter { it.paired }
        if (paired.isEmpty()) {
            list.addView(text(getString(R.string.no_hosts), 15f, false))
        }
        for (host in paired) {
            val row = LinearLayout(this).apply {
                orientation = LinearLayout.VERTICAL
                setPadding(dp(14), dp(12), dp(14), dp(12))
                setBackgroundResource(if (host.id == active) R.drawable.host_row_active else R.drawable.host_row)
                isClickable = true
                isFocusable = true
                contentDescription = host.label
                tag = host.id
                setOnClickListener { open(host) }
                setOnLongClickListener { manage(host); true }
            }
            row.addView(text(host.label + if (host.id == active) "  ·  open" else "", 17f, true))
            row.addView(text(if (host.mode == Bridge.MODE_RELAY) getString(R.string.via_relay) else host.origin, 13f, false))
            list.addView(row, LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT).apply { bottomMargin = dp(8) })
        }
        if (paired.isNotEmpty()) list.addView(text(getString(R.string.hosts_hint), 13f, false))
    }

    private fun open(host: Host) {
        startActivity(Intent(this, MainActivity::class.java).putExtra(MainActivity.EXTRA_HOST, host.id)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP))
        finish()
    }

    private fun manage(host: Host) {
        AlertDialog.Builder(this)
            .setTitle(host.label)
            .setItems(arrayOf(getString(R.string.rename), getString(R.string.remove))) { _, which ->
                if (which == 0) rename(host) else remove(host)
            }
            .show()
    }

    private fun rename(host: Host) {
        val field = EditText(this).apply { setText(host.label); setSingleLine(); setSelectAllOnFocus(true) }
        AlertDialog.Builder(this)
            .setTitle(R.string.rename)
            .setView(field)
            .setPositiveButton(android.R.string.ok) { _, _ ->
                val name = field.text.toString().trim()
                if (name.isNotEmpty()) hosts.update(host.id) { it.copy(label = name.take(60)) }
                render()
            }
            .setNegativeButton(android.R.string.cancel, null)
            .show()
    }

    private fun remove(host: Host) {
        AlertDialog.Builder(this)
            .setTitle(getString(R.string.remove_title, host.label))
            .setMessage(R.string.remove_body)
            .setPositiveButton(R.string.remove) { _, _ ->
                hosts.remove(host.id)
                render()
            }
            .setNegativeButton(android.R.string.cancel, null)
            .show()
    }

    private fun text(value: String, size: Float, bold: Boolean) = TextView(this).apply {
        text = value
        setTextColor(getColor(R.color.lectern_fg))
        setTextSize(TypedValue.COMPLEX_UNIT_SP, size)
        if (bold) typeface = Typeface.DEFAULT_BOLD
        gravity = Gravity.START
    }

    private fun dp(v: Int) = (v * resources.displayMetrics.density).toInt()
}
