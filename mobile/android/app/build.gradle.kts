import java.util.Properties
import org.jetbrains.kotlin.gradle.dsl.JvmTarget

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

// The app bundles the very same frontend build the Go binary embeds
// (web/index.html + web/static, staged by frontend/scripts/stage.py), so the
// phone runs signed code from the APK instead of code served by the relay or
// an install origin. See docs/android.md.
val webRoot = rootProject.projectDir.resolve("../../web")
val shellAssets = layout.buildDirectory.dir("generated/lectern-shell")
val copyShell by tasks.registering(Sync::class) {
    from(webRoot) {
        include("index.html", "static/**")
        exclude("static/sw.js")
    }
    into(shellAssets.map { it.dir("shell") })
}

// Android App Links: with LECTERN_APP_LINK_HOSTS (comma-separated host
// names, or the Gradle property lecternAppLinkHosts) the app claims
// https://<host>/pair and /relay-pair, so tapping a pairing link for that
// Lectern opens the app instead of the browser. Android checks the claim
// against the host's /.well-known/assetlinks.json, which every Lectern
// serves (internal/api/mobile.go). Without it, only lectern:// links open
// the app. See docs/android.md.
val appLinkHosts = (System.getenv("LECTERN_APP_LINK_HOSTS") ?: (findProperty("lecternAppLinkHosts") as String?) ?: "")
    .split(',').map { it.trim() }.filter { it.matches(Regex("^[A-Za-z0-9.-]+$")) }
val appLinksManifest = layout.buildDirectory.file("generated/app-links/AndroidManifest.xml").get().asFile
if (appLinkHosts.isNotEmpty()) {
    appLinksManifest.parentFile.mkdirs()
    appLinksManifest.writeText(
        """<?xml version="1.0" encoding="utf-8"?>
        |<manifest xmlns:android="http://schemas.android.com/apk/res/android">
        |  <application>
        |    <activity android:name="io.github.jeremiahm37.lectern.ConnectActivity">
        |      <intent-filter android:autoVerify="true">
        |        <action android:name="android.intent.action.VIEW" />
        |        <category android:name="android.intent.category.DEFAULT" />
        |        <category android:name="android.intent.category.BROWSABLE" />
        |        <data android:scheme="https" />
        |${appLinkHosts.joinToString("\n") { "        <data android:host=\"$it\" />" }}
        |        <data android:path="/pair" />
        |        <data android:path="/relay-pair" />
        |      </intent-filter>
        |    </activity>
        |  </application>
        |</manifest>
        |""".trimMargin(),
    )
}

val lecternSourceVersion = Regex("""Version\s*=\s*"(\d+)\.(\d+)\.(\d+)"""")
    .find(rootProject.projectDir.resolve("../../internal/version/version.go").readText())
    ?.let { it.groupValues.drop(1).joinToString(".") } ?: error("no version in internal/version/version.go")
// LECTERN_ANDROID_VERSION overrides it, only to build a stand-in "next
// release" for the update test (docs/android.md, "Updates").
val lecternVersion = (System.getenv("LECTERN_ANDROID_VERSION") ?: lecternSourceVersion)
val lecternVersionCode = lecternVersion.split('.').map { it.toInt() }.let { (a, b, c) -> a * 10000 + b * 100 + c }

// Release signing comes from a properties file outside the repository
// (mobile/build-android.sh points at it); without one the release APK is
// left unsigned, which is what CI should produce before its own signing step.
val signingFile = System.getenv("LECTERN_ANDROID_SIGNING")?.let { file(it) }
val signing = Properties().apply { signingFile?.takeIf { it.isFile }?.inputStream()?.use { load(it) } }

android {
    namespace = "io.github.jeremiahm37.lectern"
    compileSdk = 36

    defaultConfig {
        applicationId = "io.github.jeremiahm37.lectern"
        minSdk = 26
        targetSdk = 35
        // The app carries Lectern's own version from 2.8.0 on, so the app
        // and the host it was built with name the same release, and an app
        // update is "the next Lectern release" (Updates.kt).
        versionCode = lecternVersionCode
        versionName = lecternVersion
    }

    signingConfigs {
        if (signing.isNotEmpty()) {
            create("release") {
                storeFile = file(signing.getProperty("storeFile"))
                storePassword = signing.getProperty("storePassword")
                keyAlias = signing.getProperty("keyAlias")
                keyPassword = signing.getProperty("keyPassword")
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
            if (signing.isNotEmpty()) signingConfig = signingConfigs.getByName("release")
        }
        debug {
            applicationIdSuffix = ".debug"
        }
    }

    sourceSets["main"].assets.srcDir(shellAssets)
    if (appLinkHosts.isNotEmpty()) {
        sourceSets["debug"].manifest.srcFile(appLinksManifest)
        sourceSets["release"].manifest.srcFile(appLinksManifest)
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    buildFeatures { buildConfig = true }
    // Where the app looks for updates (Updates.kt). A published build always
    // uses the GitHub release; the update test points a build at a local copy.
    defaultConfig {
        val manifest = System.getenv("LECTERN_UPDATE_MANIFEST") ?: "https://github.com/JeremiahM37/lectern/releases/latest/download/lectern-android.json"
        val prefix = System.getenv("LECTERN_UPDATE_APK_PREFIX") ?: "https://github.com/JeremiahM37/lectern/releases/download/"
        buildConfigField("String", "UPDATE_MANIFEST", "\"$manifest\"")
        buildConfigField("String", "UPDATE_APK_PREFIX", "\"$prefix\"")
    }
    packaging { resources.excludes += setOf("META-INF/*.kotlin_module", "META-INF/versions/**") }
}

kotlin { compilerOptions { jvmTarget.set(JvmTarget.JVM_17) } }

tasks.named("preBuild") { dependsOn(copyShell) }

dependencies {
    implementation("androidx.core:core-ktx:1.16.0")
    implementation("androidx.activity:activity-ktx:1.10.1")
    implementation("androidx.webkit:webkit:1.14.0")
    implementation("androidx.work:work-runtime-ktx:2.10.3")
    implementation("org.unifiedpush.android:connector:3.3.5")
    // Already pulled in by the connector (Web Push decryption); used directly
    // for the software X25519 fallback in DeviceKey.
    implementation("com.google.crypto.tink:tink:1.23.0")
    implementation("com.journeyapps:zxing-android-embedded:4.3.0") { isTransitive = false }
    implementation("com.google.zxing:core:3.5.3")
    testImplementation("junit:junit:4.13.2")
    // android.jar only has stubs of org.json; unit tests need the real thing.
    testImplementation("org.json:json:20240303")
}
