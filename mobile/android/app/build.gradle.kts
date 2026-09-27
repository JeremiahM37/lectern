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
        versionCode = 1
        versionName = "0.1.0"
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

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    buildFeatures { buildConfig = true }
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
