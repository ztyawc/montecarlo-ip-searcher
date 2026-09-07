import java.util.Properties

plugins {
    id("com.android.application")
    id("dev.flutter.flutter-gradle-plugin")
}

val repositoryRoot = rootProject.projectDir.parentFile.parentFile
val nativeOutput = layout.buildDirectory.dir("generated/mcis/jniLibs")
val assetOutput = layout.buildDirectory.dir("generated/mcis/assets")
val sessionOutput = layout.buildDirectory.dir("generated/mcis/java")
val goExecutable = providers.gradleProperty("goExecutable").orElse("go")
val signingFile = rootProject.file("key.properties")
val signing = Properties().apply {
    if (signingFile.isFile) signingFile.inputStream().use { load(it) }
}

android {
    namespace = "com.ztyawc.mcis"
    compileSdk = 36
    ndkVersion = "28.2.13676358"
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    defaultConfig {
        applicationId = "com.ztyawc.mcis"
        minSdk = 26
        targetSdk = 35
        versionCode = flutter.versionCode
        versionName = flutter.versionName
        ndk { abiFilters += "arm64-v8a" }
    }
    if (signingFile.isFile) {
        signingConfigs.create("release") {
            storeFile = rootProject.file(signing.getProperty("storeFile"))
            storePassword = signing.getProperty("storePassword")
            keyAlias = signing.getProperty("keyAlias")
            keyPassword = signing.getProperty("keyPassword")
        }
    }
    buildTypes {
        release {
            // Missing release credentials must not silently switch to debug signing.
            if (signingFile.isFile) signingConfig = signingConfigs.getByName("release")
        }
    }
    sourceSets.getByName("main") {
        java.srcDir(sessionOutput.get().asFile)
        res.srcDir(repositoryRoot.resolve("android-app/app/src/main/res"))
        jniLibs.srcDir(nativeOutput.get().asFile)
        assets.srcDir(assetOutput.get().asFile)
    }
    packaging.jniLibs {
        useLegacyPackaging = true
        keepDebugSymbols += "**/libmcis.so"
    }
}

val buildMcisNative by tasks.registering(Exec::class) {
    workingDir(repositoryRoot)
    environment("CGO_ENABLED", "0")
    environment("GOOS", "android")
    environment("GOARCH", "arm64")
    inputs.files(fileTree(repositoryRoot) { include("cmd/**/*.go", "internal/**/*.go", "go.mod", "go.sum") })
    outputs.dir(nativeOutput)
    doFirst {
        val output = nativeOutput.get().file("arm64-v8a/libmcis.so").asFile
        output.parentFile.mkdirs()
        commandLine(goExecutable.get(), "build", "-trimpath", "-ldflags=-s -w", "-o", output.absolutePath, "./cmd/mcis")
    }
}
val syncMcisAssets by tasks.registering(Copy::class) {
    from(repositoryRoot) { include("ipv4cidr.txt", "ipv6cidr.txt") }
    into(assetOutput)
}
val syncRunSession by tasks.registering(Copy::class) {
    from(repositoryRoot.resolve("android-app/app/src/main/java")) { include("com/ztyawc/mcis/RunSession.java") }
    into(sessionOutput)
}
tasks.named("preBuild") { dependsOn(buildMcisNative, syncMcisAssets, syncRunSession) }
flutter { source = "../.." }
