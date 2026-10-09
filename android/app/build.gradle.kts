// AGP 9 compila Kotlin por sí mismo: no hace falta el plugin kotlin-android.
plugins {
    alias(libs.plugins.android.application)
}

android {
    namespace = "io.github.aerogu.lanchat"
    compileSdk = 37

    defaultConfig {
        applicationId = "io.github.aerogu.lanchat"
        minSdk = 24 // igual que androidAPI en magefiles/android.go
        targetSdk = 37
        // "go tool mage apk" pasa la versión de git (magefiles/android.go);
        // desde Android Studio queda "dev".
        versionCode = providers.gradleProperty("lanchat.versionCode").orNull?.toInt() ?: 1
        versionName = providers.gradleProperty("lanchat.versionName").orNull ?: "dev"
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
}

dependencies {
    // El núcleo en Go; lo genera "go tool mage android" (no está en el repositorio).
    implementation(files("libs/lanchat.aar"))
    implementation(libs.androidx.activity)
    implementation(libs.androidx.core.ktx)
}
