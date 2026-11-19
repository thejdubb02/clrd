package me.itsjustin.clrd

import org.junit.Assert.assertEquals
import org.junit.Test
import java.io.File

/**
 * Words the app shows live in res/values/strings.xml so they can be translated.
 * This fails when one is typed straight into the screen code again.
 */
class StringsTest {
    private val files = listOf("MainActivity.kt", "SyncService.kt")

    @Test fun noInlineUiText() {
        val found = files.flatMap { name ->
            File("src/main/java/me/itsjustin/clrd/$name").readLines()
                .filterNot { "Log." in it } // log lines are for developers, not translated
                .joinToString("\n")
                .replace(Regex("\"\"\"[\\s\\S]*?\"\"\""), "") // raw JSON bodies
                .replace(Regex("/\\*[\\s\\S]*?\\*/"), "")
                .replace(Regex("(?m)(^|\\s)//.*"), "")
                .let { Regex("\"((?:[^\"\\\\\\n]|\\\\.)*)\"").findAll(it) }
                .map { it.groupValues[1].replace(Regex("\\$\\{[^}]*}"), "") }
                .filter { it.firstOrNull()?.isUpperCase() == true || (' ' in it && it.any(Char::isLetter)) }
                .map { "$name: \"$it\"" }
                .toList()
        }
        assertEquals("move these into res/values/strings.xml", emptyList<String>(), found)
    }
}
