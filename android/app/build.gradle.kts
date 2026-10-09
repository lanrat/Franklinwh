plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

// Built from ../mobile by scripts/build-aar.sh.
val goAar = file("libs/franklinwh.aar")
if (!goAar.exists()) {
    throw GradleException("${goAar.path} is missing: run scripts/build-aar.sh first")
}

android {
    namespace = "com.github.lanrat.franklinwh.app"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.github.lanrat.franklinwh.app"
        minSdk = 26 // matches -androidapi in scripts/build-aar.sh
        targetSdk = 35
        versionCode = 1
        versionName = "0.1.0"
    }

    buildTypes {
        release {
            isMinifyEnabled = false
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
