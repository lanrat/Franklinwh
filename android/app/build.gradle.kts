plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

// Built from ../mobile by scripts/build-aar.sh.
val goAar = file("libs/franklinwh.aar")
if (!goAar.exists()) {
    throw GradleException("${goAar.path} is missing: run scripts/build-aar.sh first")
}

// Release signing comes from the environment (CI decodes it from GitHub
// secrets; see scripts/android-keystore.md), so no key material lives in the
// repository. Without it, assembleRelease produces an unsigned APK.
val releaseKeystore = System.getenv("ANDROID_KEYSTORE_FILE")?.takeIf { it.isNotEmpty() }

// CI passes the release version (-PappVersionName=0.2.1); versionCode is
// derived from it (major*10000 + minor*100 + patch) so every release can
// update the previous one.
val appVersionName = (findProperty("appVersionName") as String?) ?: "0.2.0"
val appVersionCode = appVersionName.split(".").map { it.toInt() }.let { (major, minor, patch) ->
    major * 10000 + minor * 100 + patch
}

android {
    namespace = "com.vorsk.franklinwh.app"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.vorsk.franklinwh.app"
        minSdk = 33 // Android 13; matches -androidapi in scripts/build-aar.sh
        targetSdk = 35
        versionCode = appVersionCode
        versionName = appVersionName
    }

    signingConfigs {
        if (releaseKeystore != null) {
            create("release") {
                storeFile = file(releaseKeystore)
                storePassword = System.getenv("ANDROID_KEYSTORE_PASSWORD")
                keyAlias = System.getenv("ANDROID_KEY_ALIAS")
                keyPassword = System.getenv("ANDROID_KEY_PASSWORD")
            }
        }
    }

    buildTypes {
        debug {
            // A separate app (com.vorsk.franklinwh.app.debug) that installs
            // next to the release instead of conflicting with its signature.
            applicationIdSuffix = ".debug"
            versionNameSuffix = "-debug"
        }
        release {
            isMinifyEnabled = false
            if (releaseKeystore != null) {
                signingConfig = signingConfigs.getByName("release")
            }
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
}

dependencies {
    implementation(files(goAar))
}
