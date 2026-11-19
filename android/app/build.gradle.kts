plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.compose")
    id("com.google.devtools.ksp")
}

// Release signing is opt-in: with both variables set (as in the F-Droid repo
// workflow) the release APK is signed with the project key, otherwise it stays
// unsigned. Empty counts as unset, since CI passes a missing secret as "".
val releaseKeystore = System.getenv("CLRD_KEYSTORE").orEmpty()
val releaseKeystorePassword = System.getenv("CLRD_KEYSTORE_PASSWORD").orEmpty()

android {
    namespace = "me.itsjustin.clrd"
    compileSdk = 35

    defaultConfig {
        applicationId = "me.itsjustin.clrd"
        minSdk = 26
        targetSdk = 35
        versionCode = (findProperty("versionCode") as String?)?.toInt() ?: 1
        versionName = (findProperty("versionName") as String?) ?: "0.1.0"
    }

    if (releaseKeystore.isNotEmpty() && releaseKeystorePassword.isNotEmpty()) {
        signingConfigs {
            create("release") {
                storeFile = file(releaseKeystore)
                storePassword = releaseKeystorePassword
                storeType = "pkcs12"
                keyAlias = "clrd"
                keyPassword = releaseKeystorePassword
            }
        }
    }

    buildTypes {
        release {
            // ponytail: no minify yet, smaller APK is a later win, not a v0 need.
            isMinifyEnabled = false
            signingConfig = signingConfigs.findByName("release")
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
    }

    // F-Droid: the dependency metadata blob is encrypted with a Google key and
    // breaks reproducible builds, so leave it out.
    dependenciesInfo {
        includeInApk = false
        includeInBundle = false
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }

    buildFeatures {
        compose = true
        buildConfig = true
    }

    packaging { resources { excludes += "/META-INF/{AL2.0,LGPL2.1}" } }

    // Robolectric runs Room and the NotificationManager on the JVM.
    testOptions { unitTests.isIncludeAndroidResources = true }
}

ksp { arg("room.schemaLocation", "$projectDir/schemas") }

dependencies {
    val composeBom = platform("androidx.compose:compose-bom:2024.12.01")
    implementation(composeBom)
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.material:material-icons-core")
    implementation("androidx.activity:activity-compose:1.9.3")
    implementation("androidx.core:core-ktx:1.15.0")

    implementation("androidx.room:room-runtime:2.6.1")
    implementation("androidx.room:room-ktx:2.6.1")
    ksp("androidx.room:room-compiler:2.6.1")

    implementation("com.squareup.okhttp3:okhttp:4.12.0")

    testImplementation("junit:junit:4.13.2")
    testImplementation("com.squareup.okhttp3:mockwebserver:4.12.0")
    // Android's org.json is a stub on the JVM; tests need the real one.
    testImplementation("org.json:json:20240303")
    testImplementation("org.robolectric:robolectric:4.14.1")
    testImplementation("androidx.test:core:1.6.1")
}
