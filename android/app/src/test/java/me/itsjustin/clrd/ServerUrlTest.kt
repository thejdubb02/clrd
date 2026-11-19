package me.itsjustin.clrd

import org.junit.Assert.assertEquals
import org.junit.Test

class ServerUrlTest {
    @Test fun pasteOverPrefillKeepsOnlyThePastedUrl() {
        assertEquals("https://clrd.example.org/setup?code=x", pastedUrl("https://https://clrd.example.org/setup?code=x"))
        assertEquals("http://10.0.0.5:8080", pastedUrl("https://http://10.0.0.5:8080"))
    }

    @Test fun normalTypingIsLeftAlone() {
        assertEquals("https://", pastedUrl("https://"))
        assertEquals("https://clrd.exa", pastedUrl("https://clrd.exa"))
        val nested = "https://a.example/setup?next=https://b.example"
        assertEquals(nested, pastedUrl(nested))
        assertEquals("https://a.example", serverAddress(nested))
    }

    @Test fun saveCutsALinkDownToTheServer() {
        assertEquals("https://clrd.example.org", serverAddress("https://https://clrd.example.org/setup?code=abc"))
        assertEquals("https://clrd.example.org", serverAddress(" HTTPS://Clrd.Example.org/app/cleared#x "))
        assertEquals("https://clrd.example.org", serverAddress("clrd.example.org/"))
        assertEquals("http://192.168.1.4:8080", serverAddress("http://192.168.1.4:8080/api/v1/topics"))
    }

    @Test fun saveKeepsAMountPath() {
        assertEquals("https://home.example.org/clrd", serverAddress("https://home.example.org/clrd/app"))
        assertEquals("https://home.example.org/clrd", serverAddress("https://home.example.org/clrd/"))
    }
}
