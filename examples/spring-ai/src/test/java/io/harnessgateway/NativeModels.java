package io.harnessgateway;

import java.util.Arrays;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/** Choose explicit catalog IDs for live per-request override acceptance. */
final class NativeModels {
    private NativeModels() { }

    static Map<String, String> select(List<String> configured, List<String> catalog, String overrides) {
        var aliases = configured.stream().map(String::trim).map(model -> model.split("/", 2)[0]).distinct().toList();
        var requested = new LinkedHashMap<String, String>();
        for (var model : Arrays.stream(overrides.split(",")).map(String::trim).filter(value -> !value.isEmpty()).toList()) {
            var parts = model.split("/", 2);
            if (parts.length != 2 || !concrete(parts[0], model) || !aliases.contains(parts[0]) || !catalog.contains(model)) {
                throw new IllegalArgumentException("Native override must be a discovered concrete ID for a selected harness (not auto): " + model);
            }
            if (requested.putIfAbsent(parts[0], model) != null) {
                throw new IllegalArgumentException("Specify only one native override per harness: " + parts[0]);
            }
        }
        var selected = new LinkedHashMap<String, String>();
        for (var alias : aliases) {
            var model = requested.get(alias);
            if (model == null) {
                model = configured.stream().map(String::trim).filter(id -> concrete(alias, id)).findFirst()
                    .orElseGet(() -> catalog.stream().filter(id -> concrete(alias, id)).sorted().findFirst()
                        .orElseThrow(() -> new IllegalStateException("No discovered native model for " + alias)));
            }
            if (!catalog.contains(model)) throw new IllegalArgumentException("Native model is absent from discovery: " + model);
            selected.put(alias, model);
        }
        return selected;
    }

    private static boolean concrete(String alias, String id) {
        return id.startsWith(alias + "/") && id.length() > alias.length() + 1 && !id.equalsIgnoreCase(alias + "/auto");
    }
}
