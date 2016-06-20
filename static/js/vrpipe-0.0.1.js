// we have a custom ko binding to handle our loading feedback
// (from http://blog.greatrexpectations.com/2012/06/17/loading-placeholders-using-knockout-js/)
ko.bindingHandlers.loadingWhen = {
    init: function (element) {
        var $element = $(element),
            currentPosition = $element.css("position")
            $loader = $("<div>").addClass("loader").hide();
        
        //add the loader
        $element.append($loader);
        
        //make sure that we can absolutely position the loader against the original element
        if (currentPosition == "auto" || currentPosition == "static")
            $element.css("position", "relative");

        //center the loader
        $loader.css({
            position: "absolute",
            top: "20px",
            left: "50%",
            "margin-left": -($loader.width() / 2) + "px"
        });
    },
    update: function (element, valueAccessor) {
        var isLoading = ko.utils.unwrapObservable(valueAccessor()),
            $element = $(element),
            $childrenToHide = $element.children(":not(div.loader)"),
            $loader = $element.find("div.loader");

        if (isLoading) {
            $childrenToHide.css("visibility", "hidden").attr("disabled", "disabled");
            $loader.fadeIn("slow");
        }
        else {
            $loader.fadeOut("fast");
            $childrenToHide.css("visibility", "visible").removeAttr("disabled");
        }
    }
};

// round floats in a standard way, eliminating trailing zeroes and dots
var rounder = function(i) {
    return parseFloat(parseFloat(i).toFixed(2));
};

