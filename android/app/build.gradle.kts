// AGP 9 compila Kotlin por sí mismo: no hace falta el plugin kotlin-android.
plugins {
    alias(libs.plugins.android.application)
}

// Versión de git, igual que el .exe (magefiles: appVersion): "1.2.0" en la
// etiqueta v1.2.0, "1.2.0-3-gabc1234" después, "-dirty" con cambios sin commit.
val gitVersion: String = providers.exec {
    commandLine("git", "describe", "--tags", "--always", "--dirty")
    isIgnoreExitValue = true
}.standardOutput.asText.get().trim().removePrefix("v").ifEmpty { "dev" }

// versionCode: el número que Android compara para permitir una actualización
// (debe crecer siempre): 1.2.3-45-gabc → 1_02_003_045; sin etiqueta, 1.
fun versionCodeOf(v: String): Int {
    val m = Regex("""^(\d+)\.(\d+)\.(\d+)(?:-(\d+)-g[0-9a-f]+)?""").find(v) ?: return 1
    val n = m.groupValues.drop(1).map { it.toIntOrNull() ?: 0 }
    return maxOf(1, n[0] * 10_000_000 + n[1] * 100_000 + n[2] * 1_000 + minOf(n[3], 999))
}

// Firma de la versión publicada. La clave vive fuera del proyecto y sirve
// para otras apps (docs/ANDROID.md, "Clave de firma"): sus datos van en
// ~/.gradle/gradle.properties (aerogu.signing.*) o, en GitHub Actions, en
// variables de entorno (SIGNING_*). Sin ellos, el release sale sin firmar.
fun signing(prop: String, env: String): String? =
    providers.gradleProperty("aerogu.signing.$prop").orElse(providers.environmentVariable(env)).orNull

android {
    namespace = "io.github.aerogu.lanchat"
    compileSdk = 37

    defaultConfig {
        applicationId = "io.github.aerogu.lanchat"
        minSdk = 24 // igual que androidAPI en magefiles/android.go
        targetSdk = 37
        versionCode = versionCodeOf(gitVersion)
        versionName = gitVersion
    }

    signingConfigs {
        signing("storeFile", "SIGNING_STORE_FILE")?.let { store ->
            create("release") {
                storeFile = file(store)
                storePassword = signing("storePassword", "SIGNING_STORE_PASSWORD")
                keyAlias = signing("keyAlias", "SIGNING_KEY_ALIAS")
                keyPassword = signing("keyPassword", "SIGNING_KEY_PASSWORD")
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            signingConfig = signingConfigs.findByName("release")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    lint {
        // Los textos están en español; el corrector de lint es en inglés.
        disable += "Typos"
    }
}

dependencies {
    // El núcleo en Go; lo genera "go tool mage android" (no está en el repositorio).
    implementation(files("libs/lanchat.aar"))
    implementation(libs.androidx.activity)
    implementation(libs.androidx.core.ktx)
}
