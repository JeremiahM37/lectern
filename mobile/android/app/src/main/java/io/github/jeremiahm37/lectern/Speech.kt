package io.github.jeremiahm37.lectern

import android.content.Context
import android.content.Intent
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.speech.RecognitionListener
import android.speech.RecognizerIntent
import android.speech.SpeechRecognizer
import android.speech.tts.TextToSpeech
import android.speech.tts.UtteranceProgressListener
import org.json.JSONArray
import org.json.JSONObject
import java.util.Locale

/**
 * The phone's own speech recognition and text-to-speech, for the page
 * (frontend/src/native/speech.ts). A WebView has neither: its
 * webkitSpeechRecognition exists but has no service behind it, and its
 * speechSynthesis speaks nothing. The page sees these through the same
 * SpeechRecognition / speechSynthesis shapes a browser has, so dictation
 * and voice mode work unchanged.
 *
 * Recognition is one utterance at a time, as Android's recognizer is; the
 * page restarts it for continuous listening. Events go to the page as
 * "lectern-native-speech" ({session, type: partial|final|error|end, text,
 * error}) and "lectern-native-tts" ({id, type: start|end|error|voices}).
 */
class Speech(context: Context, private val emit: (event: String, detail: JSONObject) -> Unit) {
    private val app = context.applicationContext
    private val main = Handler(Looper.getMainLooper())
    private var recognizer: SpeechRecognizer? = null
    /** The page's session the recognizer is listening for. */
    private var session = 0
    private var listening = false
    /** The newest words heard in this utterance, not yet final. */
    private var heard = ""

    private var tts: TextToSpeech? = null
    private var ttsReady = false
    private var ttsFailed = false
    private val waiting = mutableListOf<() -> Unit>()

    fun recognitionAvailable(): Boolean =
        SpeechRecognizer.isRecognitionAvailable(app) ||
            (Build.VERSION.SDK_INT >= 33 && SpeechRecognizer.isOnDeviceRecognitionAvailable(app))

    fun ttsAvailable(): Boolean =
        !ttsFailed && app.packageManager.queryIntentServices(Intent(TextToSpeech.Engine.INTENT_ACTION_TTS_SERVICE), 0).isNotEmpty()

    // ---- recognition (main thread only) ----

    fun listen(session: Int, lang: String) = main.post {
        val rec = recognizer ?: create()?.also { recognizer = it }
        if (rec == null) {
            event(session, "error", error = "service-not-allowed")
            event(session, "end")
            return@post
        }
        if (listening) rec.cancel()
        this.session = session
        listening = true
        heard = ""
        val intent = Intent(RecognizerIntent.ACTION_RECOGNIZE_SPEECH).apply {
            putExtra(RecognizerIntent.EXTRA_LANGUAGE_MODEL, RecognizerIntent.LANGUAGE_MODEL_FREE_FORM)
            putExtra(RecognizerIntent.EXTRA_PARTIAL_RESULTS, true)
            putExtra(RecognizerIntent.EXTRA_MAX_RESULTS, 1)
            putExtra(RecognizerIntent.EXTRA_CALLING_PACKAGE, app.packageName)
            if (lang.isNotBlank()) putExtra(RecognizerIntent.EXTRA_LANGUAGE, lang)
            // A person dictating pauses to think; don't cut them off at the
            // first breath.
            putExtra(RecognizerIntent.EXTRA_SPEECH_INPUT_COMPLETE_SILENCE_LENGTH_MILLIS, 2000L)
            putExtra(RecognizerIntent.EXTRA_SPEECH_INPUT_POSSIBLY_COMPLETE_SILENCE_LENGTH_MILLIS, 1500L)
        }
        runCatching { rec.startListening(intent) }.onFailure {
            listening = false
            event(session, "error", error = "audio-capture")
            event(session, "end")
        }
    }

    /** Stops listening and keeps what was heard (a final result follows). */
    fun stop() = main.post { if (listening) recognizer?.stopListening() }

    /** Stops listening and drops what was heard. */
    fun cancel() = main.post {
        if (!listening) return@post
        listening = false
        recognizer?.cancel()
        event(session, "end")
    }

    /** The person refused the microphone; tell the page that asked. */
    fun refused(session: Int) = main.post {
        event(session, "error", error = "not-allowed")
        event(session, "end")
    }

    private fun create(): SpeechRecognizer? {
        val rec = when {
            SpeechRecognizer.isRecognitionAvailable(app) -> SpeechRecognizer.createSpeechRecognizer(app)
            Build.VERSION.SDK_INT >= 33 && SpeechRecognizer.isOnDeviceRecognitionAvailable(app) ->
                SpeechRecognizer.createOnDeviceSpeechRecognizer(app)
            else -> null
        } ?: return null
        rec.setRecognitionListener(object : RecognitionListener {
            override fun onReadyForSpeech(params: Bundle?) = event(session, "start")
            override fun onBeginningOfSpeech() {}
            override fun onRmsChanged(rmsdB: Float) {}
            override fun onBufferReceived(buffer: ByteArray?) {}
            override fun onEndOfSpeech() {}
            override fun onEvent(eventType: Int, params: Bundle?) {}

            override fun onPartialResults(partial: Bundle?) {
                val text = first(partial) ?: return
                if (text.isBlank()) return
                heard = text
                event(session, "partial", text = text)
            }

            override fun onResults(results: Bundle?) {
                listening = false
                (first(results)?.takeIf { it.isNotBlank() } ?: heard.takeIf { it.isNotBlank() })
                    ?.let { event(session, "final", text = it) }
                heard = ""
                event(session, "end")
            }

            override fun onError(error: Int) {
                // cancel() already told the page; a late error is not news.
                if (!listening) return
                listening = false
                val name = errorName(error)
                // Some recognizers show words as they hear them and then
                // end with "no match". What was shown on screen was heard:
                // keep it rather than make the person say it again.
                if (name == "no-speech" && heard.isNotBlank()) event(session, "final", text = heard)
                else event(session, "error", error = name)
                heard = ""
                event(session, "end")
            }
        })
        return rec
    }

    private fun first(bundle: Bundle?): String? =
        bundle?.getStringArrayList(SpeechRecognizer.RESULTS_RECOGNITION)?.firstOrNull()

    private fun event(session: Int, type: String, text: String? = null, error: String? = null) {
        val detail = JSONObject().put("session", session).put("type", type)
        if (text != null) detail.put("text", text)
        if (error != null) detail.put("error", error)
        emit("lectern-native-speech", detail)
    }

    // ---- text to speech ----

    private fun withTts(then: () -> Unit) = main.post {
        if (ttsReady) return@post then()
        if (ttsFailed) return@post
        waiting += then
        if (tts != null) return@post
        tts = TextToSpeech(app) { status ->
            main.post {
                if (status != TextToSpeech.SUCCESS) {
                    ttsFailed = true
                    waiting.clear()
                    return@post
                }
                ttsReady = true
                tts?.setOnUtteranceProgressListener(object : UtteranceProgressListener() {
                    override fun onStart(id: String) = say(id, "start")
                    override fun onDone(id: String) = say(id, "end")
                    @Deprecated("Deprecated in Java")
                    override fun onError(id: String) = say(id, "error")
                    override fun onError(id: String, errorCode: Int) = say(id, "error")
                    override fun onStop(id: String, interrupted: Boolean) = say(id, "error", "interrupted")
                })
                emit("lectern-native-tts", JSONObject().put("type", "voices"))
                waiting.toList().also { waiting.clear() }.forEach { it() }
            }
        }
    }

    /** Starts the engine early, so its voices are listed before the first word. */
    fun warmTts() = withTts {}

    fun speak(id: String, text: String, lang: String, rate: Float, voice: String) = withTts {
        val engine = tts ?: return@withTts
        val chosen = voice.takeIf { it.isNotBlank() }?.let { name -> engine.voices?.firstOrNull { it.name == name } }
        if (chosen != null) engine.voice = chosen
        else if (lang.isNotBlank()) engine.language = Locale.forLanguageTag(lang)
        engine.setSpeechRate(rate.coerceIn(0.1f, 4f))
        if (engine.speak(text, TextToSpeech.QUEUE_ADD, null, id) != TextToSpeech.SUCCESS) say(id, "error")
    }

    fun stopSpeaking() = main.post { tts?.stop() }

    /** [{name, lang}] of the installed voices that need no download. */
    fun voices(): String {
        val engine = tts.takeIf { ttsReady } ?: return "[]"
        val list = runCatching { engine.voices }.getOrNull().orEmpty()
            .filter { !it.isNetworkConnectionRequired && !it.features.contains(TextToSpeech.Engine.KEY_FEATURE_NOT_INSTALLED) }
            .sortedBy { it.name }
        return JSONArray(list.map { JSONObject().put("name", it.name).put("lang", it.locale.toLanguageTag()) }).toString()
    }

    private fun say(id: String, type: String, error: String? = null) {
        val detail = JSONObject().put("id", id).put("type", type)
        if (error != null) detail.put("error", error)
        emit("lectern-native-tts", detail)
    }

    fun destroy() = main.post {
        recognizer?.destroy()
        recognizer = null
        listening = false
        tts?.shutdown()
        tts = null
        ttsReady = false
        waiting.clear()
    }

    companion object {
        /** Android's recognizer errors, as the Web Speech API names them
         * (the page's error handling already knows these). */
        fun errorName(code: Int): String = when (code) {
            SpeechRecognizer.ERROR_NO_MATCH, SpeechRecognizer.ERROR_SPEECH_TIMEOUT -> "no-speech"
            SpeechRecognizer.ERROR_INSUFFICIENT_PERMISSIONS -> "not-allowed"
            SpeechRecognizer.ERROR_AUDIO -> "audio-capture"
            SpeechRecognizer.ERROR_NETWORK, SpeechRecognizer.ERROR_NETWORK_TIMEOUT, SpeechRecognizer.ERROR_SERVER -> "network"
            SpeechRecognizer.ERROR_CLIENT -> "aborted"
            SpeechRecognizer.ERROR_RECOGNIZER_BUSY -> "busy"
            12, 13 -> "language-not-supported" // ERROR_LANGUAGE_NOT_SUPPORTED / _UNAVAILABLE (API 31)
            else -> "unknown"
        }
    }
}
