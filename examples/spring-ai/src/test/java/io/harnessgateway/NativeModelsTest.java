package io.harnessgateway;

import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.Test;
import static org.junit.jupiter.api.Assertions.*;

class NativeModelsTest {
    private static final List<String> CATALOG = List.of("codex", "copilot", "codex/native-two", "codex/native-one", "copilot/auto", "copilot/native-one");

    @Test void discoversOneStableNativeIdPerHarness() {
        assertEquals(Map.of("codex", "codex/native-one", "copilot", "copilot/native-one"),
            NativeModels.select(List.of("codex", "copilot"), CATALOG, ""));
    }

    @Test void explicitSelectionsOverrideAutomaticDiscovery() {
        assertEquals(Map.of("codex", "codex/native-two", "copilot", "copilot/native-one"),
            NativeModels.select(List.of("codex", "copilot"), CATALOG, " codex/native-two "));
    }

    @Test void reusesConfiguredNativeModelsWithoutDuplicatingHarnesses() {
        assertEquals(Map.of("codex", "codex/native-two"),
            NativeModels.select(List.of("codex", "codex/native-two"), CATALOG, ""));
    }

    @Test void invalidSelectionsFailWithoutSilentFallback() {
        for (var overrides : List.of("codex", "codex/", "codex/missing", "copilot/native-one", "codex/native-one,codex/native-two")) {
            assertThrows(IllegalArgumentException.class, () -> NativeModels.select(List.of("codex"), CATALOG, overrides), overrides);
        }
        assertThrows(IllegalArgumentException.class, () -> NativeModels.select(List.of("codex/missing"), CATALOG, ""));
    }

    @Test void missingNativeCatalogFailsInsteadOfSkippingLiveCoverage() {
        assertThrows(IllegalStateException.class, () -> NativeModels.select(List.of("codex"), List.of("codex", "copilot/native-one"), ""));
    }

    @Test void autoRoutingCannotMasqueradeAsConcreteNativeSelection() {
        assertEquals(Map.of("copilot", "copilot/native-one"), NativeModels.select(List.of("copilot/auto"), CATALOG, ""));
        assertThrows(IllegalArgumentException.class, () -> NativeModels.select(List.of("copilot"), CATALOG, "copilot/auto"));
        assertThrows(IllegalStateException.class, () -> NativeModels.select(List.of("copilot"), List.of("copilot", "copilot/auto"), ""));
    }
}
