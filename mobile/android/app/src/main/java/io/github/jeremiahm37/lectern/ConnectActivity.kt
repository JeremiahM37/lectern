package io.github.jeremiahm37.lectern

import android.content.Intent
import android.graphics.Typeface
import android.os.Bundle
import android.text.InputType
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
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions

/**
 * First run (and after disconnecting): scan the pairing QR code Lectern shows
 * in Settings → Devices, or paste its link. The pairing itself then runs in
 * the app's own copy of the Lectern pages (/relay-pair or /pair).
 */
class ConnectActivity : ComponentActivity() {
    private lateinit var input: EditText
    private lateinit var error: TextView

    private val scan = registerForActivityResult(ScanContract()) { result ->
        result.contents?.let {
            input.setText(it)
            connect(it)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val fg = getColor(R.color.lectern_fg)
        val pad = dp(24)
        val column = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(pad, pad, pad, pad)
            gravity = Gravity.CENTER_HORIZONTAL
        }
        column.addView(TextView(this).apply {
            text = getString(R.string.connect_title)
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
    }

    private fun connect(text: String) {
        val link = Link.parse(text)
        if (link == null) {
            error.text = getString(R.string.bad_link)
            return
        }
        // A new connection replaces the old one entirely, keys included.
        Bridge.forget(this)
        val store = SecureStore(this)
        val start = when (link) {
            is Link.Relay -> Shell.APP_ORIGIN + "/relay-pair#p=" + link.fragment
            is Link.DirectPair -> {
                store.mode = Bridge.MODE_DIRECT
                store.origin = link.origin
                link.origin + "/pair#code=" + link.code
            }
            is Link.Direct -> {
                store.mode = Bridge.MODE_DIRECT
                store.origin = link.origin
                link.origin + "/"
            }
        }
        startActivity(Intent(this, MainActivity::class.java).putExtra(MainActivity.EXTRA_START, start)
            .addFlags(Intent.FLAG_ACTIVITY_CLEAR_TASK or Intent.FLAG_ACTIVITY_NEW_TASK))
        finish()
    }

    private fun dp(v: Int) = (v * resources.displayMetrics.density).toInt()
}
