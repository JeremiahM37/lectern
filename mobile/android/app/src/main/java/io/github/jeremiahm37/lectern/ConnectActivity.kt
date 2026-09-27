package io.github.jeremiahm37.lectern

import android.content.Intent
import android.graphics.Typeface
import android.os.Bundle
import android.text.InputType
import android.util.TypedValue
import android.view.Gravity
import android.view.View
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import androidx.activity.ComponentActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions

/**
 * Pairing a Lectern: on first run, from the list of Lecterns ("Add a
 * Lectern"), or from a pairing link tapped anywhere on the phone
 * (lectern://pair?…, or the https link itself when the app was built to
 * claim that address). A link only fills in the form: nothing is paired and
 * nothing already paired changes until the person taps Connect. The pairing
 * itself then runs in the app's own copy of the Lectern pages (/relay-pair
 * or /pair).
 */
class ConnectActivity : ComponentActivity() {
    private lateinit var input: EditText
    private lateinit var error: TextView
    private lateinit var from: TextView
    private lateinit var hosts: Hosts

    private val scan = registerForActivityResult(ScanContract()) { result ->
        result.contents?.let {
            input.setText(it)
            connect(it)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        hosts = Hosts(this)
        val fg = getColor(R.color.lectern_fg)
        val pad = dp(24)
        val column = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(pad, pad, pad, pad)
            gravity = Gravity.CENTER_HORIZONTAL
        }
        val adding = hosts.all().any { it.paired }
        column.addView(TextView(this).apply {
            text = getString(if (adding) R.string.add_title else R.string.connect_title)
            setTextColor(fg)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 24f)
            typeface = Typeface.DEFAULT_BOLD
        })
        column.addView(TextView(this).apply {
            text = getString(R.string.connect_body)
            setTextColor(fg)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 15f)
            setPadding(0, dp(12), 0, dp(20))
        })
        from = TextView(this).apply {
            id = R.id.link_from
            setTextColor(getColor(R.color.lectern_accent))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 15f)
            setPadding(0, 0, 0, dp(16))
            visibility = View.GONE
        }
        column.addView(from)
        column.addView(Button(this).apply {
            id = R.id.scan
            text = getString(R.string.scan)
            setOnClickListener {
                scan.launch(ScanOptions().setDesiredBarcodeFormats(ScanOptions.QR_CODE).setBeepEnabled(false)
                    .setOrientationLocked(false).setPrompt("Scan the QR code from Lectern's Settings → Devices"))
            }
        }, LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT))
        input = EditText(this).apply {
            id = R.id.link
            hint = getString(R.string.paste_hint)
            setTextColor(fg)
            setHintTextColor(0x99E6E9F2.toInt())
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI
            isSingleLine = true
        }
        column.addView(input, LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT).apply { topMargin = dp(20) })
        column.addView(Button(this).apply {
            id = R.id.connect
            text = getString(R.string.connect)
            setOnClickListener { connect(input.text.toString()) }
        }, LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT))
        if (adding) {
            column.addView(Button(this).apply {
                id = R.id.cancel
                text = getString(R.string.back_to_lecterns)
                setOnClickListener { finish() }
            }, LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT).apply { topMargin = dp(8) })
        }
        error = TextView(this).apply {
            setTextColor(0xFFFF8A80.toInt())
            setPadding(0, dp(12), 0, 0)
        }
        column.addView(error)
        val scroll = ScrollView(this).apply { addView(column) }
        ViewCompat.setOnApplyWindowInsetsListener(scroll) { v, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.ime())
            v.setPadding(bars.left, bars.top, bars.right, bars.bottom)
            WindowInsetsCompat.CONSUMED
        }
        setContentView(scroll)
        prefill(intent)
    }

    // A link tapped while this screen is already open (warm start).
    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        prefill(intent)
    }

    /** Fills the form from a tapped pairing link, and says where it leads. */
    private fun prefill(intent: Intent?) {
        val data = intent?.takeIf { it.action == Intent.ACTION_VIEW }?.dataString ?: return
        val link = Link.parse(data)
        if (link == null) {
            error.text = getString(R.string.bad_link)
            return
        }
        input.setText(data)
        error.text = ""
        from.text = getString(R.string.link_from, link.describe())
        from.visibility = View.VISIBLE
    }

    private fun connect(text: String) {
        val link = Link.parse(text)
        if (link == null) {
            error.text = getString(R.string.bad_link)
            return
        }
        // Adding a Lectern never touches the ones already paired. An entry
        // left by an earlier, abandoned attempt goes first.
        hosts.pruneUnpaired()
        val (host, start) = when (link) {
            is Link.Relay -> {
                val host = hosts.add(Bridge.MODE_RELAY, "", "")
                host to host.origin + "/relay-pair#p=" + link.fragment
            }
            is Link.DirectPair -> {
                val host = hosts.byOrigin(link.origin) ?: hosts.add(Bridge.MODE_DIRECT, link.origin, Hosts.labelFor(Bridge.MODE_DIRECT, link.origin, null))
                host to link.origin + "/pair#code=" + link.code
            }
            is Link.Direct -> {
                val host = hosts.byOrigin(link.origin) ?: hosts.add(Bridge.MODE_DIRECT, link.origin, Hosts.labelFor(Bridge.MODE_DIRECT, link.origin, null))
                // Nothing to pair: this Lectern admits the phone by identity.
                hosts.update(host.id) { it.copy(paired = true) }
                host to link.origin + "/"
            }
        }
        hosts.activeId = host.id
        startActivity(Intent(this, MainActivity::class.java)
            .putExtra(MainActivity.EXTRA_START, start)
            .putExtra(MainActivity.EXTRA_HOST, host.id)
            .addFlags(Intent.FLAG_ACTIVITY_CLEAR_TASK or Intent.FLAG_ACTIVITY_NEW_TASK))
        finish()
    }

    private fun dp(v: Int) = (v * resources.displayMetrics.density).toInt()
}
